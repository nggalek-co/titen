package app

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"titen/internal/saas"
	"titen/internal/storage"
	"titen/internal/testhelper"
	"titen/internal/transcript"
	"titen/internal/transcription"
)

func serviceFixture(t *testing.T, config testhelper.CaptureConfig) (*Service, Settings) {
	t.Helper()
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	helper, _ := testhelper.Capture(t, config)
	s := NewService(store)
	s.finishTimeout = 100 * time.Millisecond
	s.run = func(ctx context.Context, chunks <-chan []byte, notify func(transcription.Update)) error {
		for {
			select {
			case _, ok := <-chunks:
				if !ok {
					return nil
				}
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	return s, Settings{HelperPath: helper}
}

func awaitIdle(t *testing.T, s *Service) {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if s.Snapshot().State == Idle {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("meeting did not finish: %+v", s.Snapshot())
}

func serviceAudioFrames() []byte {
	const samples = 1600
	frames := make([]byte, 0, 20*(24+samples*2))
	for i := 0; i < 20; i++ {
		frame := make([]byte, 24+samples*2)
		copy(frame[:4], "TIT1")
		frame[4], frame[5] = 1, 1
		binary.LittleEndian.PutUint16(frame[6:8], 1)
		binary.LittleEndian.PutUint32(frame[8:12], 16000)
		binary.LittleEndian.PutUint64(frame[12:20], uint64(time.Duration(i)*100*time.Millisecond))
		binary.LittleEndian.PutUint32(frame[20:24], samples*2)
		frames = append(frames, frame...)
	}
	return frames
}

func TestStartCapturesBeforeRemoteAuthorization(t *testing.T) {
	s, cfg := serviceFixture(t, testhelper.CaptureConfig{Frames: serviceAudioFrames()})
	entered := make(chan struct{})
	release := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	s.authorize = func(context.Context, *saas.Client, string) (saas.Session, error) {
		close(entered)
		<-release
		return saas.Session{}, nil
	}

	started := make(chan error, 1)
	go func() { started <- s.Start(context.Background(), cfg, "key", "meeting") }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("authorization was not reached")
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(cfg.HelperPath + ".args"); err == nil && len(s.chunks) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := os.Stat(cfg.HelperPath + ".args"); err != nil || len(s.chunks) == 0 || s.Snapshot().State != Recording || s.Snapshot().TranscriptionStatus != transcription.ConnectionConnecting {
		t.Fatalf("audio was not captured while authorization was pending: state=%s status=%d buffered=%d", s.Snapshot().State, s.Snapshot().TranscriptionStatus, len(s.chunks))
	}
	close(release)
	released = true
	if err := <-started; err != nil {
		t.Fatal(err)
	}
	if err := s.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestCaptureFailureFinishesAndAllowsRestart(t *testing.T) {
	s, cfg := serviceFixture(t, testhelper.CaptureConfig{Stderr: "permission denied\n", ExitImmediately: true, ExitCode: 1})
	ctx := context.Background()
	if err := s.Start(ctx, cfg, "key", "failed"); err != nil {
		t.Fatal(err)
	}
	awaitIdle(t, s)
	if s.Snapshot().Error == "" {
		t.Fatal("capture failure was hidden")
	}
	meetings, err := s.Store.Meetings(ctx, "")
	if err != nil || len(meetings) != 1 || meetings[0].EndedAt == nil {
		t.Fatalf("unfinished meeting %v %v", meetings, err)
	}
	if err = s.Start(ctx, cfg, "key", "retry"); err != nil {
		t.Fatal(err)
	}
	awaitIdle(t, s)
}

func TestStreamFailureMarksMeetingInterrupted(t *testing.T) {
	s, cfg := serviceFixture(t, testhelper.CaptureConfig{})
	s.run = func(ctx context.Context, chunks <-chan []byte, notify func(transcription.Update)) error {
		return errors.New("provider disconnected")
	}
	if err := s.Start(context.Background(), cfg, "key", "interrupted"); err != nil {
		t.Fatal(err)
	}
	awaitIdle(t, s)
	meetings, err := s.Store.Meetings(context.Background(), "")
	if err != nil || len(meetings) != 1 || meetings[0].InterruptedAt == nil {
		t.Fatalf("meeting interruption not saved: meetings=%+v err=%v", meetings, err)
	}
}

func TestPauseResumeAndStopDrainsDeduplicatedFinals(t *testing.T) {
	s, cfg := serviceFixture(t, testhelper.CaptureConfig{})
	ctx := context.Background()
	s.run = func(ctx context.Context, chunks <-chan []byte, notify func(transcription.Update)) error {
		for range chunks {
		}
		update := transcription.Update{Finals: []transcript.Segment{{Text: "final words", Speaker: "1", StartMS: 1, EndMS: 10}}}
		notify(update)
		notify(update)
		return nil
	}
	if err := s.Start(ctx, cfg, "key", "meeting"); err != nil {
		t.Fatal(err)
	}
	id := s.Snapshot().MeetingID
	first := s.capture
	if err := s.Pause(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-first.Frames:
	default:
		t.Fatal("paused helper still running")
	}
	if s.Snapshot().State != Paused {
		t.Fatal(s.Snapshot())
	}
	if err := s.Resume(ctx); err != nil {
		t.Fatal(err)
	}
	if s.Snapshot().MeetingID != id {
		t.Fatal("resume replaced meeting")
	}
	if err := s.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	snap := s.Snapshot()
	rows, err := s.Store.Segments(ctx, id)
	if err != nil || len(rows) != 1 || len(snap.Segments) != 1 || snap.Partial != "" || snap.State != Idle {
		t.Fatalf("snapshot=%+v rows=%v err=%v", snap, rows, err)
	}
}

func TestStopBoundsUncooperativeNetworkAndRejectsLateUpdate(t *testing.T) {
	s, cfg := serviceFixture(t, testhelper.CaptureConfig{})
	release := make(chan struct{})
	exited := make(chan struct{})
	s.run = func(ctx context.Context, chunks <-chan []byte, notify func(transcription.Update)) error {
		defer close(exited)
		<-release
		notify(transcription.Update{Finals: []transcript.Segment{{Text: "late"}}})
		return nil
	}
	ctx := context.Background()
	if err := s.Start(ctx, cfg, "key", "meeting"); err != nil {
		t.Fatal(err)
	}
	stopped := make(chan error, 1)
	go func() { stopped <- s.Stop(ctx) }()
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Stop hung after cancellation")
	}
	close(release)
	<-exited
	if len(s.Snapshot().Segments) != 0 || s.Snapshot().Error == "" {
		t.Fatal(s.Snapshot())
	}
}

func TestCaptureWarningDoesNotFailMeeting(t *testing.T) {
	s, cfg := serviceFixture(t, testhelper.CaptureConfig{
		Frames: serviceAudioFrames(),
		Stderr: "Warning: Audio device reported a capture discontinuity. Some audio may be missing.\n",
	})
	ctx := context.Background()
	if err := s.Start(ctx, cfg, "key", "warning remains nonfatal"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if snapshot := s.Snapshot(); snapshot.State != Recording || snapshot.Error != "" {
		t.Fatalf("nonfatal helper warning failed capture: %+v", snapshot)
	}
	s.op.Lock()
	restarts := s.captureRestarts
	s.op.Unlock()
	if restarts != 0 {
		t.Fatalf("nonfatal helper warning triggered %d capture restarts", restarts)
	}
	if err := s.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestCaptureInterruptionRecoversSameMeeting(t *testing.T) {
	s, cfg := serviceFixture(t, testhelper.CaptureConfig{Interruptions: 2})
	ctx := context.Background()
	if err := s.Start(ctx, cfg, "key", "meeting"); err != nil {
		t.Fatal(err)
	}
	id := s.Snapshot().MeetingID
	deadline := time.Now().Add(8 * time.Second)
	recovered := false
	for time.Now().Before(deadline) {
		s.op.Lock()
		recovered = s.captureRestarts == 2
		s.op.Unlock()
		if recovered && s.Snapshot().State == Recording {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !recovered || s.Snapshot().State != Recording || s.Snapshot().MeetingID != id {
		t.Fatalf("recovery failed: %+v", s.Snapshot())
	}
	if err := s.ConfigureCapture(cfg); err == nil {
		t.Fatal("changed capture during recording")
	}
	if err := s.Pause(); err != nil {
		t.Fatal(err)
	}
	cfg.MicrophoneID = "another-device"
	if err := s.ConfigureCapture(cfg); err != nil {
		t.Fatal(err)
	}
	if s.settings.MicrophoneID != "another-device" {
		t.Fatal("paused device change not applied")
	}
	if err := s.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	meetings, err := s.Store.Meetings(ctx, "")
	if err != nil || len(meetings) != 1 || meetings[0].InterruptedAt == nil {
		t.Fatalf("recovered gap was not marked incomplete: meetings=%+v err=%v", meetings, err)
	}
}

func TestHeartbeatStopsOnExhaustedCredit(t *testing.T) {
	s, cfg := serviceFixture(t, testhelper.CaptureConfig{})
	var calls atomic.Int32
	s.beatInterval = 10 * time.Millisecond
	s.beat = func(ctx context.Context, sessionID string) (saas.Heartbeat, error) {
		if calls.Add(1) == 1 {
			return saas.Heartbeat{OK: true, MinutesLeft: 42}, nil
		}
		return saas.Heartbeat{OK: false, Reason: "insufficient_funds"}, nil
	}
	ctx := context.Background()
	if err := s.Start(ctx, cfg, "key", "meeting"); err != nil {
		t.Fatal(err)
	}
	awaitIdle(t, s)
	snap := s.Snapshot()
	if !strings.Contains(snap.Error, "credit") {
		t.Fatalf("snapshot=%+v", snap)
	}
}

func TestHeartbeatSurvivesTransientFailure(t *testing.T) {
	s, cfg := serviceFixture(t, testhelper.CaptureConfig{})
	var calls atomic.Int32
	s.beatInterval = 10 * time.Millisecond
	s.beat = func(ctx context.Context, sessionID string) (saas.Heartbeat, error) {
		if calls.Add(1) == 1 {
			return saas.Heartbeat{}, context.DeadlineExceeded
		}
		return saas.Heartbeat{OK: true, MinutesLeft: 42}, nil
	}
	ctx := context.Background()
	if err := s.Start(ctx, cfg, "key", "meeting"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if s.Snapshot().MinutesKnown {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := s.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	snap := s.Snapshot()
	if snap.Error != "" {
		t.Fatalf("transient heartbeat failure surfaced as an error: %+v", snap)
	}
}
