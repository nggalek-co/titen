package audio

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
	"titen/internal/testhelper"
)

func TestCaptureReportsOverflowWhileRunning(t *testing.T) {
	helper, _ := testhelper.Capture(t, testhelper.CaptureConfig{
		Frames: bytes.Repeat(wire(1, 16000, 1, []int16{1}), 100),
		Stderr: "Native audio writer queue overflow; captured audio was dropped.\n",
	})
	capture, err := StartCapture(context.Background(), CaptureConfig{HelperPath: helper})
	if err != nil {
		t.Fatal(err)
	}
	defer capture.Close()
	deadline := time.After(3 * time.Second)
	native, queue := false, false
	for !native || !queue {
		select {
		case err, ok := <-capture.Errors:
			if !ok {
				t.Fatal("capture exited before reporting overflow")
			}
			native = native || strings.Contains(err.Error(), "Native audio writer queue overflow")
			queue = queue || strings.Contains(err.Error(), "audio frame queue overflow")
		case <-deadline:
			t.Fatalf("missing live warnings, native=%v queue=%v", native, queue)
		}
	}
	select {
	case <-capture.done:
		t.Fatal("warnings were delivered only after capture exited")
	default:
	}
}

func TestCaptureDrainsFinalFramesOnStop(t *testing.T) {
	helper, _ := testhelper.Capture(t, testhelper.CaptureConfig{FinalFrames: wire(1, 16000, 1, []int16{42})})
	capture, err := StartCapture(context.Background(), CaptureConfig{HelperPath: helper})
	if err != nil {
		t.Fatal(err)
	}
	if err := capture.Close(); err != nil {
		t.Fatal(err)
	}
	count := 0
	for frame := range capture.Frames {
		if len(frame.Samples) != 1 || frame.Samples[0] != 42 {
			t.Fatalf("unexpected final samples %v", frame.Samples)
		}
		count++
	}
	if count != 1 {
		t.Fatalf("got %d final frames after graceful stop", count)
	}
}

func TestCaptureBoundsUncooperativeHelper(t *testing.T) {
	helper, _ := testhelper.Capture(t, testhelper.CaptureConfig{IgnoreStop: true, Frames: wire(1, 16000, 1, []int16{1})})
	capture, err := StartCapture(context.Background(), CaptureConfig{HelperPath: helper})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-capture.Frames:
	case <-time.After(5 * time.Second):
		t.Fatal("helper did not start")
	}
	done := make(chan struct{})
	go func() { capture.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("uncooperative helper blocked stop")
	}
}
