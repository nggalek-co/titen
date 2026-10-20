package audio

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCaptureReportsOverflowWhileRunning(t *testing.T) {
	dir := t.TempDir()
	packets := bytes.Repeat(wire(1, 16000, 1, []int16{1}), 100)
	if err := os.WriteFile(filepath.Join(dir, "frames"), packets, 0600); err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(dir, "helper")
	script := `#!/bin/sh
printf 'Native audio writer queue overflow; captured audio was dropped.\n' >&2
cat "$(dirname "$0")/frames"
exec sleep 30
`
	if err := os.WriteFile(helper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
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
