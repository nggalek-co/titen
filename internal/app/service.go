package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"titen/internal/audio"
	"titen/internal/storage"
	"titen/internal/transcript"
	"titen/internal/transcription"
)

type State string

const (
	Idle      State = "Ready"
	Starting  State = "Starting"
	Recording State = "Recording"
	Paused    State = "Paused"
	Stopping  State = "Finishing"
)

type Snapshot struct {
	State                        State
	MeetingID                    string
	Partial                      string
	Segments                     []transcript.Segment
	Duration                     time.Duration
	MicrophoneLevel, SystemLevel float64
	Error                        string
}

type Service struct {
	Store           *storage.Store
	op              sync.Mutex
	mu              sync.Mutex
	state           Snapshot
	settings        Settings
	capture         *audio.Capture
	captureDone     chan struct{}
	chunks          chan []byte
	networkDone     chan struct{}
	cancel          context.CancelFunc
	began           time.Time
	elapsed         time.Duration
	run             func(context.Context, <-chan []byte, func(transcription.Update)) error
	finishTimeout   time.Duration
	seen            map[string]bool
	captureRestarts int
}

func NewService(store *storage.Store) *Service {
	return &Service{Store: store, state: Snapshot{State: Idle}}
}
func (s *Service) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.state
	v.Segments = append([]transcript.Segment(nil), v.Segments...)
	v.Duration = s.elapsed
	if v.State == Recording {
		v.Duration += time.Since(s.began)
	}
	return v
}
func (s *Service) fail(err error) {
	if err == nil {
		return
	}
	s.mu.Lock()
	s.state.Error = err.Error()
	id, state := s.state.MeetingID, s.state.State
	s.mu.Unlock()
	slog.Warn("meeting issue", "meeting_id", id, "state", state)
}

func (s *Service) Start(ctx context.Context, cfg Settings, key, title string) error {
	s.op.Lock()
	defer s.op.Unlock()
	if s.Snapshot().State != Idle {
		return errors.New("a meeting is already active")
	}
	if strings.TrimSpace(key) == "" {
		return errors.New("save a Soniox API key in Settings first")
	}
	s.mu.Lock()
	s.state = Snapshot{State: Starting}
	s.mu.Unlock()
	meeting, err := s.Store.CreateMeeting(ctx, title)
	if err != nil {
		s.setIdle()
		return err
	}
	if cfg.AutoSync {
		if err := s.Store.RequestDocument(ctx, meeting.ID); err != nil {
			s.setIdle()
			_ = s.Store.FinishMeeting(ctx, meeting.ID)
			return err
		}
	}
	s.mu.Lock()
	s.state.MeetingID = meeting.ID
	s.elapsed = 0
	s.settings = cfg
	s.seen = make(map[string]bool)
	s.captureRestarts = 0
	s.mu.Unlock()
	streamCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.chunks = make(chan []byte, 250)
	s.networkDone = make(chan struct{})
	chunks, done := s.chunks, s.networkDone
	go func() {
		defer close(done)
		client := transcription.Client{APIKey: key, SampleRate: audio.SampleRate, Languages: strings.Split(cfg.Languages, ",")}
		run := s.run
		if run == nil {
			run = client.Run
		}
		err := run(streamCtx, chunks, func(update transcription.Update) {
			s.mu.Lock()
			active := s.state.MeetingID == meeting.ID && s.state.State != Idle
			s.mu.Unlock()
			if !active {
				return
			}
			if update.Err != nil {
				s.fail(update.Err)
			}
			for i := range update.Finals {
				update.Finals[i].MeetingID = meeting.ID
			}
			if len(update.Finals) > 0 {
				if err := s.Store.AddSegments(context.Background(), update.Finals); err != nil {
					s.fail(fmt.Errorf("save transcript: %w", err))
					cancel()
					return
				}
			}
			s.mu.Lock()
			defer s.mu.Unlock()
			if s.state.MeetingID != meeting.ID || s.state.State == Idle {
				return
			}
			s.state.Partial = update.Partial
			for _, segment := range update.Finals {
				if !s.seen[segment.Key()] {
					s.seen[segment.Key()] = true
					s.state.Segments = append(s.state.Segments, segment)
					if len(s.state.Segments) > 500 {
						old := s.state.Segments[0]
						delete(s.seen, old.Key())
						s.state.Segments = append([]transcript.Segment(nil), s.state.Segments[1:]...)
					}
				}
			}
		})
		go func() {
			s.op.Lock()
			defer s.op.Unlock()
			snap := s.Snapshot()
			if snap.MeetingID != meeting.ID || snap.State == Idle {
				return
			}
			if err != nil && streamCtx.Err() == nil {
				s.fail(err)
			}
			if snap.Error == "" && err == nil {
				s.fail(errors.New("transcription stream ended; meeting was stopped"))
			}
			_ = s.stop(context.Background())
		}()
	}()
	if err = s.beginCapture(streamCtx); err != nil {
		close(s.chunks)
		cancel()
		s.waitNetwork(context.Background(), done)
		s.fail(err)
		s.setIdle()
		_ = s.Store.FinishMeeting(ctx, meeting.ID)
		return err
	}
	slog.Info("meeting started", "meeting_id", meeting.ID)
	return nil
}
func (s *Service) beginCapture(ctx context.Context) error {
	cap, err := audio.StartCapture(ctx, audio.CaptureConfig{HelperPath: s.settings.HelperPath, MicrophoneID: s.settings.MicrophoneID, Mode: s.settings.CaptureMode})
	if err != nil {
		return err
	}
	s.capture = cap
	s.captureDone = make(chan struct{})
	done := s.captureDone
	chunks := s.chunks
	s.mu.Lock()
	s.state.State = Recording
	s.began = time.Now()
	s.mu.Unlock()
	go func() {
		defer close(done)
		errorsDone := make(chan struct{})
		go func() {
			defer close(errorsDone)
			for err := range cap.Errors {
				s.fail(err)
			}
		}()
		err := (audio.Pipeline{}).Run(ctx, cap.Frames, func(chunk audio.Chunk) error {
			s.mu.Lock()
			s.state.MicrophoneLevel = chunk.MicrophoneLevel
			s.state.SystemLevel = chunk.SystemLevel
			s.mu.Unlock()
			select {
			case chunks <- chunk.PCM:
			default:
				s.fail(errors.New("transcription is behind; audio was dropped from the bounded buffer"))
			}
			return nil
		})
		if err != nil && ctx.Err() == nil {
			s.fail(err)
		}
		<-errorsDone
		go func() {
			s.op.Lock()
			defer s.op.Unlock()
			if s.capture == cap {
				snap := s.Snapshot()
				if snap.State == Recording && strings.Contains(snap.Error, "Capture interrupted") && s.captureRestarts < 3 && ctx.Err() == nil {
					s.captureRestarts++
					s.mu.Lock()
					s.elapsed += time.Since(s.began)
					s.state.State = Paused
					s.state.Error = fmt.Sprintf("Capture interrupted. Recovery attempt %d of 3", s.captureRestarts)
					s.mu.Unlock()
					s.capture = nil
					if err := s.beginCapture(ctx); err == nil {
						return
					} else {
						s.fail(err)
					}
				}
				if s.Snapshot().Error == "" {
					s.fail(errors.New("audio capture ended; meeting was stopped"))
				}
				_ = s.stop(context.Background())
			}
		}()
	}()
	return nil
}
func (s *Service) Pause() error {
	s.op.Lock()
	defer s.op.Unlock()
	if s.Snapshot().State != Recording {
		return errors.New("recording is not active")
	}
	s.endCapture()
	s.mu.Lock()
	s.elapsed += time.Since(s.began)
	s.state.State = Paused
	s.mu.Unlock()
	slog.Info("meeting paused", "meeting_id", s.Snapshot().MeetingID)
	return nil
}
func (s *Service) Resume(ctx context.Context) error {
	s.op.Lock()
	defer s.op.Unlock()
	if s.Snapshot().State != Paused {
		return errors.New("meeting is not paused")
	}
	return s.beginCapture(ctx)
}
func (s *Service) endCapture() {
	if s.capture != nil {
		_ = s.capture.Close()
		<-s.captureDone
		s.capture = nil
	}
	s.mu.Lock()
	s.state.MicrophoneLevel = 0
	s.state.SystemLevel = 0
	s.mu.Unlock()
}
func (s *Service) Stop(ctx context.Context) error {
	s.op.Lock()
	defer s.op.Unlock()
	return s.stop(ctx)
}
func (s *Service) stop(ctx context.Context) error {
	snap := s.Snapshot()
	if snap.State == Idle {
		return nil
	}
	s.mu.Lock()
	if s.state.State == Recording {
		s.elapsed += time.Since(s.began)
	}
	s.state.State = Stopping
	s.mu.Unlock()
	s.endCapture()
	close(s.chunks)
	s.waitNetwork(ctx, s.networkDone)
	s.cancel()
	err := s.Store.FinishMeeting(context.Background(), snap.MeetingID)
	s.setIdle()
	slog.Info("meeting stopped", "meeting_id", snap.MeetingID)
	return err
}
func (s *Service) setIdle() { s.mu.Lock(); s.state.State = Idle; s.state.Partial = ""; s.mu.Unlock() }

func (s *Service) waitNetwork(ctx context.Context, done <-chan struct{}) {
	timeout := s.finishTimeout
	if timeout == 0 {
		timeout = 15 * time.Second
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		s.fail(errors.New("Soniox finalization timed out; unfinalized speech may be missing"))
	case <-ctx.Done():
		s.fail(ctx.Err())
	}
}

func (s *Service) ConfigureCapture(cfg Settings) error {
	s.op.Lock()
	defer s.op.Unlock()
	state := s.Snapshot().State
	if state != Idle && state != Paused {
		return errors.New("pause recording before changing capture settings")
	}
	s.settings.MicrophoneID = cfg.MicrophoneID
	s.settings.HelperPath = cfg.HelperPath
	s.settings.CaptureMode = cfg.CaptureMode
	return nil
}
