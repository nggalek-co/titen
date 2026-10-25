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
		Stderr: "Warning: Native audio writer queue overflow; captured audio was dropped.\n",
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
			queue = queue || strings.Contains(err.Error(), "audio frame queue overflow")
		case warning, ok := <-capture.Warnings:
			if !ok {
				t.Fatal("capture exited before reporting native warning")
			}
			native = native || strings.Contains(warning.Error(), "Native audio writer queue overflow")
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

func TestCaptureTreatsLegacyDiscontinuityAsWarning(t *testing.T) {
	helper, _ := testhelper.Capture(t, testhelper.CaptureConfig{
		Stderr:          "Audio device reported a capture discontinuity. Some audio may be missing.\n",
		ExitImmediately: true,
	})
	capture, err := StartCapture(context.Background(), CaptureConfig{HelperPath: helper})
	if err != nil {
		t.Fatal(err)
	}
	defer capture.Close()
	var warning, fatal bool
	for err := range capture.Errors {
		fatal = fatal || strings.Contains(err.Error(), "capture discontinuity")
	}
	for diagnostic := range capture.Warnings {
		warning = warning || strings.Contains(diagnostic.Error(), "capture discontinuity")
	}
	if !warning || fatal {
		t.Fatalf("warning=%v fatal=%v", warning, fatal)
	}
}

func TestLimitedErrorClassifiesSplitWarningLine(t *testing.T) {
	var errors, warnings []error
	writer := &limitedError{
		report: func(err error) { errors = append(errors, err) },
		warn:   func(err error) { warnings = append(warnings, err) },
	}
	for _, chunk := range []string{"Warning: Audio device reported ", "a capture discontinuity.\n"} {
		if _, err := writer.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	writer.flush()
	if len(errors) != 0 || len(warnings) != 1 || !strings.Contains(warnings[0].Error(), "capture discontinuity") {
		t.Fatalf("errors=%v warnings=%v", errors, warnings)
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
