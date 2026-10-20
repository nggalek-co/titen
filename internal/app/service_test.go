package app

import (
	"context"
	"path/filepath"
	"testing"
	"time"
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

func TestCaptureInterruptionRecoversSameMeeting(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "interrupted")
	s, cfg := serviceFixture(t, testhelper.CaptureConfig{InterruptMarker: marker})
	ctx := context.Background()
	if err := s.Start(ctx, cfg, "key", "meeting"); err != nil {
		t.Fatal(err)
	}
	id := s.Snapshot().MeetingID
	deadline := time.Now().Add(3 * time.Second)
	recovered := false
	for time.Now().Before(deadline) {
		s.op.Lock()
		recovered = s.captureRestarts == 1
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
}
