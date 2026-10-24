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
	"titen/internal/saas"
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

const preAuthorizationAudioChunks = 1250

type Snapshot struct {
	State                        State
	MeetingID                    string
	Partial                      string
	Segments                     []transcript.Segment
	Duration                     time.Duration
	MicrophoneLevel, SystemLevel float64
	MinutesLeft                  int
	MinutesKnown                 bool
	TranscriptionStatus          transcription.ConnectionStatus
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
	authorize       func(context.Context, *saas.Client, string) (saas.Session, error)
	beat            func(context.Context, string) (saas.Heartbeat, error)
	beatInterval    time.Duration
	sessionID       string
	incomplete      bool
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
	slog.Warn("meeting issue", "meeting_id", id, "state", state, "error", err)
}

func (s *Service) failIncomplete(err error) {
	s.mu.Lock()
	s.incomplete = true
	s.mu.Unlock()
	s.fail(err)
}

func (s *Service) Start(ctx context.Context, cfg Settings, key, title string) error {
	s.op.Lock()
	defer s.op.Unlock()
	if s.Snapshot().State != Idle {
		return errors.New("a meeting is already active")
	}
	if strings.TrimSpace(key) == "" {
		return errors.New("sign in to your Titen account in Settings first")
	}
	s.mu.Lock()
	s.state = Snapshot{State: Starting, TranscriptionStatus: transcription.ConnectionConnecting}
	s.mu.Unlock()
	client := &saas.Client{BaseURL: cfg.ServerEndpoint, Token: key}
	var session saas.Session
	meeting, err := s.Store.CreateMeeting(ctx, title)
	if err != nil {
		s.setIdle()
		return err
	}
	s.mu.Lock()
	s.state = Snapshot{State: Starting, MeetingID: meeting.ID, TranscriptionStatus: transcription.ConnectionConnecting}
	s.state.MinutesKnown = false
	s.elapsed = 0
	s.settings = cfg
	s.seen = make(map[string]bool)
	s.sessionID = ""
	s.captureRestarts = 0
	s.incomplete = false
	s.mu.Unlock()
	streamCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.chunks = make(chan []byte, preAuthorizationAudioChunks)
	chunks := s.chunks
	finishFailedStart := func(cause error) error {
		if s.capture != nil {
			s.endCapture()
		}
		close(chunks)
		cancel()
		s.failIncomplete(cause)
		markErr := s.Store.MarkMeetingInterrupted(context.Background(), meeting.ID)
		finishErr := s.Store.FinishMeeting(context.Background(), meeting.ID)
		s.setIdle()
		return errors.Join(cause, markErr, finishErr)
	}
	if err := s.beginCapture(streamCtx); err != nil {
		return finishFailedStart(err)
	}

	authorize := s.authorize
	if authorize == nil && s.run == nil {
		authorize = func(ctx context.Context, client *saas.Client, title string) (saas.Session, error) {
			return client.Authorize(ctx, title)
		}
	}
	if authorize != nil {
		session, err = authorize(ctx, client, title)
		if err != nil {
			return finishFailedStart(err)
		}
	}
	cleanupSession := func() {
		if session.SessionID != "" {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cleanupCancel()
			_ = client.End(cleanupCtx, session)
		}
	}
	if session.MeetingID != "" {
		if err = s.Store.SetCloudMeeting(ctx, meeting.ID, cfg.ServerEndpoint, session.MeetingID); err != nil {
			cleanupSession()
			return finishFailedStart(err)
		}
	}
	s.mu.Lock()
	s.sessionID = session.SessionID
	s.mu.Unlock()

	s.networkDone = make(chan struct{})
	done := s.networkDone
	if beat := s.beat; beat != nil || session.SessionID != "" {
		if beat == nil {
			beat = client.Beat
		}
		interval := s.beatInterval
		if interval <= 0 {
			interval = 30 * time.Second
		}
		go s.heartbeat(streamCtx, cancel, meeting.ID, beat, interval)
	}
	go func() {
		defer close(done)
		run := s.run
		if run == nil {
			run = func(ctx context.Context, chunks <-chan []byte, notify func(transcription.Update)) error {
				return client.StreamMeeting(ctx, session, chunks, notify, func(id string) {
					s.mu.Lock()
					if s.state.MeetingID == meeting.ID {
						s.sessionID = id
					}
					s.mu.Unlock()
				})
			}
		}
		err := run(streamCtx, chunks, func(update transcription.Update) {
			s.mu.Lock()
			active := s.state.MeetingID == meeting.ID && s.state.State != Idle
			if active && update.ConnectionStatus != transcription.ConnectionUnknown {
				s.state.TranscriptionStatus = update.ConnectionStatus
			}
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
		if err != nil && streamCtx.Err() == nil {
			s.failIncomplete(err)
		}
		go func() {
			s.op.Lock()
			defer s.op.Unlock()
			snap := s.Snapshot()
			if snap.MeetingID != meeting.ID || snap.State == Idle {
				return
			}
			if err == nil {
				if snap.Error == "" {
					s.failIncomplete(errors.New("transcription stream ended; meeting was stopped"))
				} else {
					s.mu.Lock()
					s.incomplete = true
					s.mu.Unlock()
				}
			}
			_ = s.stop(context.Background())
		}()
	}()
	slog.Info("meeting started", "meeting_id", meeting.ID)
	return nil
}

func (s *Service) heartbeat(ctx context.Context, cancel context.CancelFunc, meetingID string, beat func(context.Context, string) (saas.Heartbeat, error), interval time.Duration) {
	timer := time.NewTicker(interval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		s.mu.Lock()
		sessionID := s.sessionID
		s.mu.Unlock()
		res, err := beat(ctx, sessionID)
		if err != nil {
			if ctx.Err() == nil {
				slog.Warn("transcription heartbeat failed", "error", err)
			}
			continue
		}
		s.mu.Lock()
		if s.state.MeetingID == meetingID && s.state.State != Idle {
			s.state.MinutesLeft = res.MinutesLeft
			s.state.MinutesKnown = true
		}
		s.mu.Unlock()
		if res.OK {
			continue
		}
		if res.Reason == "insufficient_funds" {
			s.failIncomplete(errors.New("Titen credit exhausted; top up your account on the website"))
		} else {
			s.failIncomplete(errors.New("Titen session ended on the server"))
		}
		cancel()
		return
	}
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
				s.failIncomplete(errors.New("transcription is behind; audio was dropped from the bounded buffer"))
			}
			return nil
		})
		if err != nil && ctx.Err() == nil {
			s.fail(err)
		}
		<-errorsDone
		go s.recoverCapture(ctx, cap, s.Snapshot().MeetingID)
	}()
	return nil
}

func (s *Service) recoverCapture(ctx context.Context, failed *audio.Capture, meetingID string) {
	s.op.Lock()
	if s.capture != failed || s.Snapshot().State != Recording || ctx.Err() != nil {
		s.op.Unlock()
		return
	}
	issue := s.Snapshot().Error
	s.capture = nil
	if requiresCapturePermissionFix(issue) {
		s.mu.Lock()
		s.incomplete = true
		s.mu.Unlock()
		_ = failed.Close()
		s.op.Unlock()
		_ = s.Stop(context.Background())
		return
	}
	s.captureRestarts++
	retry := time.Second
	for n := 1; n < s.captureRestarts && retry < 30*time.Second; n++ {
		retry = min(retry*2, 30*time.Second)
	}
	s.mu.Lock()
	s.elapsed += time.Since(s.began)
	s.began = time.Now()
	s.incomplete = true
	s.state.Error = "Audio capture was interrupted; retrying automatically"
	s.mu.Unlock()
	_ = failed.Close()
	s.op.Unlock()

	for ctx.Err() == nil {
		timer := time.NewTimer(retry)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}

		s.op.Lock()
		snap := s.Snapshot()
		if snap.MeetingID != meetingID || snap.State != Recording || s.capture != nil || ctx.Err() != nil {
			s.op.Unlock()
			return
		}
		s.mu.Lock()
		s.elapsed += time.Since(s.began)
		s.began = time.Now()
		s.mu.Unlock()
		if err := s.beginCapture(ctx); err == nil {
			s.mu.Lock()
			if strings.HasPrefix(s.state.Error, "Audio capture was interrupted;") || strings.HasPrefix(s.state.Error, "Audio capture unavailable;") {
				s.state.Error = ""
			}
			s.mu.Unlock()
			attempt := s.captureRestarts
			s.op.Unlock()
			slog.Info("audio capture recovered", "meeting_id", meetingID, "attempt", attempt)
			return
		} else {
			s.captureRestarts++
			s.mu.Lock()
			s.state.Error = fmt.Sprintf("Audio capture unavailable; retrying automatically: %v", err)
			s.mu.Unlock()
			s.op.Unlock()
			retry = min(retry*2, 30*time.Second)
		}
	}
}

func requiresCapturePermissionFix(message string) bool {
	message = strings.ToLower(message)
	for _, marker := range []string{"permission denied", "access denied", "not authorized", "tcc"} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
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
	s.mu.Lock()
	incomplete := s.incomplete
	s.mu.Unlock()
	if incomplete {
		if err := s.Store.MarkMeetingInterrupted(context.Background(), snap.MeetingID); err != nil {
			s.fail(fmt.Errorf("mark interrupted meeting: %w", err))
		}
	}
	err := s.Store.FinishMeeting(context.Background(), snap.MeetingID)
	s.setIdle()
	slog.Info("meeting stopped", "meeting_id", snap.MeetingID)
	return err
}
func (s *Service) setIdle() {
	s.mu.Lock()
	s.state.State = Idle
	s.state.Partial = ""
	s.state.TranscriptionStatus = transcription.ConnectionUnknown
	s.mu.Unlock()
}

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
		s.failIncomplete(errors.New("Server finalization timed out; unfinalized speech may be missing"))
	case <-ctx.Done():
		s.failIncomplete(ctx.Err())
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
