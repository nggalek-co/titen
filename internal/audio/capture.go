package audio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Device struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type CaptureConfig struct{ HelperPath, MicrophoneID, Mode string }
type Capture struct {
	Frames <-chan Frame
	Errors <-chan error
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once
}

func ListDevices(ctx context.Context, helperPath string) ([]Device, error) {
	out, err := exec.CommandContext(ctx, helperPath, "--list-devices").Output()
	if err != nil {
		return nil, fmt.Errorf("list microphones: %w", err)
	}
	var d []Device
	err = json.Unmarshal(out, &d)
	return d, err
}
func StartCapture(ctx context.Context, cfg CaptureConfig) (*Capture, error) {
	if cfg.HelperPath == "" {
		return nil, errors.New("audio helper path is required")
	}
	ctx, cancel := context.WithCancel(ctx)
	args := []string{}
	switch cfg.Mode {
	case "Microphone only":
		args = append(args, "--microphone-only")
	case "System audio only":
		args = append(args, "--system-only")
	case "", "Both":
	default:
		cancel()
		return nil, errors.New("unsupported capture mode")
	}
	if cfg.MicrophoneID != "" {
		args = append(args, "--microphone", cfg.MicrophoneID)
	}
	cmd := exec.CommandContext(ctx, cfg.HelperPath, args...)
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 2 * time.Second
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	errs := make(chan error, 32)
	report := func(err error) {
		select {
		case errs <- err:
		default:
		}
	}
	stderr := &limitedError{report: report}
	cmd.Stderr = stderr
	if err = cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("start native audio helper: %w", err)
	}
	frames := make(chan Frame, 32)
	c := &Capture{Frames: frames, Errors: errs, cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(c.done)
		defer close(frames)
		defer close(errs)
		var readErr error
		overflowReported := false
		for {
			f, e := ReadFrame(stdout)
			if e != nil {
				readErr = e
				if !errors.Is(e, io.EOF) {
					_ = cmd.Process.Kill()
				}
				break
			}
			select {
			case frames <- f:
			default:
				if !overflowReported {
					report(errors.New("audio frame queue overflow; captured audio was dropped"))
					overflowReported = true
				}
				select {
				case <-frames:
				default:
				}
				select {
				case frames <- f:
				default:
				}
			}
		}
		waitErr := cmd.Wait()
		finalReport := func(err error) {
			select {
			case errs <- err:
			default:
				select {
				case <-errs:
				default:
				}
				report(err)
			}
		}
		if ctx.Err() == nil {
			if waitErr != nil {
				finalReport(fmt.Errorf("audio capture stopped: %w; %s", waitErr, stderr.String()))
			} else if readErr != nil && !errors.Is(readErr, io.EOF) {
				finalReport(readErr)
			}
		}
	}()
	return c, nil
}
func (c *Capture) Close() error { c.once.Do(c.cancel); <-c.done; return nil }

type limitedError struct {
	mu     sync.Mutex
	b      []byte
	report func(error)
}

func (l *limitedError) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := len(p)
	if left := 4096 - len(l.b); left > 0 {
		l.b = append(l.b, p[:min(left, n)]...)
	}
	if l.report != nil {
		if message := strings.TrimSpace(string(p[:min(n, 4096)])); message != "" {
			l.report(fmt.Errorf("audio helper: %s", message))
		}
	}
	return n, nil
}
func (l *limitedError) String() string { l.mu.Lock(); defer l.mu.Unlock(); return string(l.b) }
