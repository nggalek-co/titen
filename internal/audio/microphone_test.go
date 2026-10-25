package audio

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
	"titen/internal/testhelper"
)

func microphoneFixture(t *testing.T, source byte, config testhelper.CaptureConfig) (CaptureConfig, string) {
	t.Helper()
	config.Frames = wire(source, 16000, 1, []int16{16384, -16384})
	helper, _ := testhelper.Capture(t, config)
	return CaptureConfig{HelperPath: helper, MicrophoneID: "chosen-device"}, helper
}

func TestMicrophoneUsesSelectedDeviceAndReportsLocalLevel(t *testing.T) {
	cfg, helper := microphoneFixture(t, 1, testhelper.CaptureConfig{
		Stderr: "Audio device reported a capture discontinuity. Some audio may be missing.\n",
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	level := float64(0)
	result, err := TestMicrophone(ctx, cfg, func(v float64) { level = v; cancel() })
	if err != nil || result.Frames != 1 || level != 0.5 || result.Peak != 0.5 {
		t.Fatalf("result=%+v level=%f err=%v", result, level, err)
	}
	args, err := os.ReadFile(helper + ".args")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(args), "--microphone-only\n--microphone\nchosen-device") {
		t.Fatalf("wrong capture arguments %s", args)
	}
}
func TestMicrophoneIgnoresSystemAudioAndReportsNoInput(t *testing.T) {
	cfg, _ := microphoneFixture(t, 2, testhelper.CaptureConfig{})
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	result, err := TestMicrophone(ctx, cfg, func(float64) { t.Error("system audio reached microphone meter") })
	if err == nil || result.Frames != 0 || !strings.Contains(err.Error(), "no microphone samples") {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}
func TestMicrophoneShowsPermissionFailure(t *testing.T) {
	cfg, _ := microphoneFixture(t, 1, testhelper.CaptureConfig{Stderr: "Microphone access denied\n", ExitImmediately: true, ExitCode: 1})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := TestMicrophone(ctx, cfg, nil)
	if err == nil || !strings.Contains(err.Error(), "Microphone access denied") {
		t.Fatalf("permission failure hidden: %v", err)
	}
}
