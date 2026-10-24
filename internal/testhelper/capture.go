package testhelper

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

type CaptureConfig struct {
	Frames          []byte
	FinalFrames     []byte
	Stderr          string
	ExitCode        int
	ExitImmediately bool
	IgnoreStop      bool
	Interruptions   int
}

var fixture struct {
	sync.Once
	path string
	err  error
}

func Capture(t *testing.T, config CaptureConfig) (string, string) {
	t.Helper()
	fixture.Do(func() {
		dir, err := os.MkdirTemp("", "titen-capture-fixture-")
		if err != nil {
			fixture.err = err
			return
		}
		_, source, _, _ := runtime.Caller(0)
		fixture.path = filepath.Join(dir, "helper.exe")
		cmd := exec.Command("go", "build", "-o", fixture.path, filepath.Join(filepath.Dir(source), "capturefixture"))
		output, err := cmd.CombinedOutput()
		if err != nil {
			fixture.err = &buildError{err, string(output)}
		}
	})
	if fixture.err != nil {
		t.Fatal(fixture.err)
	}
	dir := t.TempDir()
	helper := filepath.Join(dir, "helper.exe")
	data, err := os.ReadFile(fixture.path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(helper, data, 0700); err != nil {
		t.Fatal(err)
	}
	data, err = json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(helper+".json", data, 0600); err != nil {
		t.Fatal(err)
	}
	return helper, dir
}

type buildError struct {
	err    error
	output string
}

func (e *buildError) Error() string { return e.err.Error() + "\n" + e.output }
