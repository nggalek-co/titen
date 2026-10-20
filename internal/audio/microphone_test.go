package audio

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func microphoneFixture(t *testing.T, source byte, script string) (CaptureConfig, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "frames"), wire(source, 16000, 1, []int16{16384, -16384}), 0600); err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(dir, "helper")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\n"+script), 0700); err != nil {
		t.Fatal(err)
	}
	return CaptureConfig{HelperPath: helper, MicrophoneID: "chosen-device"}, dir
}

func TestMicrophoneUsesSelectedDeviceAndReportsLocalLevel(t *testing.T) {
	cfg, dir := microphoneFixture(t, 1, `printf '%s\n' "$@" > "$(dirname "$0")/args"
cat "$(dirname "$0")/frames"
exec sleep 30
`)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	level := float64(0)
	result, err := TestMicrophone(ctx, cfg, func(v float64) { level = v; cancel() })
	if err != nil || result.Frames != 1 || level != 0.5 || result.Peak != 0.5 {
		t.Fatalf("result=%+v level=%f err=%v", result, level, err)
	}
	args, err := os.ReadFile(filepath.Join(dir, "args"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(args), "--microphone-only\n--microphone\nchosen-device") {
		t.Fatalf("wrong capture arguments %s", args)
	}
}
func TestMicrophoneIgnoresSystemAudioAndReportsNoInput(t *testing.T) {
	cfg, _ := microphoneFixture(t, 2, `cat "$(dirname "$0")/frames"
exec sleep 30
`)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	result, err := TestMicrophone(ctx, cfg, func(float64) { t.Error("system audio reached microphone meter") })
	if err == nil || result.Frames != 0 || !strings.Contains(err.Error(), "no microphone samples") {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}
func TestMicrophoneShowsPermissionFailure(t *testing.T) {
	cfg, _ := microphoneFixture(t, 1, "echo 'Microphone access denied' >&2\nexit 1\n")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := TestMicrophone(ctx, cfg, nil)
	if err == nil || !strings.Contains(err.Error(), "Microphone access denied") {
		t.Fatalf("permission failure hidden: %v", err)
	}
}
