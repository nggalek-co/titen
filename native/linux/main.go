//go:build linux

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	sampleRate   = 48000
	channels     = 1
	frameSamples = sampleRate / 50
	frameBytes   = frameSamples * channels * 2
	queueFrames  = 32
	micSource    = 1
	systemSource = 2
)

type device struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type pactlSource struct {
	Name          string `json:"name"`
	Description   string `json:"description"`
	MonitorSource string `json:"monitor_source"`
}

func parseSources(data []byte) ([]device, error) {
	var sources []pactlSource
	if err := json.Unmarshal(data, &sources); err != nil {
		return nil, fmt.Errorf("read PulseAudio source list: %w", err)
	}
	devices := make([]device, 0, len(sources))
	for _, source := range sources {
		if source.Name == "" || source.MonitorSource != "" || strings.HasSuffix(source.Name, ".monitor") {
			continue
		}
		name := source.Description
		if name == "" {
			name = source.Name
		}
		devices = append(devices, device{ID: source.Name, Name: name})
	}
	return devices, nil
}

func encodeFrame(source byte, offset time.Duration, samples []int16) []byte {
	payload := len(samples) * 2
	frame := make([]byte, 24+payload)
	copy(frame, "TIT1")
	frame[4] = source
	frame[5] = 1
	binary.LittleEndian.PutUint16(frame[6:], channels)
	binary.LittleEndian.PutUint32(frame[8:], sampleRate)
	binary.LittleEndian.PutUint64(frame[12:], uint64(offset))
	binary.LittleEndian.PutUint32(frame[20:], uint32(payload))
	for i, sample := range samples {
		binary.LittleEndian.PutUint16(frame[24+i*2:], uint16(sample))
	}
	return frame
}

func listSources() ([]device, error) {
	path, err := exec.LookPath("pactl")
	if err != nil {
		return nil, errors.New("pactl is unavailable; install pulseaudio-utils")
	}
	out, err := exec.Command(path, "-f", "json", "list", "sources").Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && len(bytes.TrimSpace(exit.Stderr)) > 0 {
			return nil, fmt.Errorf("list PulseAudio sources: %s", strings.TrimSpace(string(exit.Stderr)))
		}
		return nil, fmt.Errorf("list PulseAudio sources: %w", err)
	}
	return parseSources(out)
}

func listDevices() error {
	devices, err := listSources()
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(devices)
}

func hasDevice(devices []device, id string) bool {
	for _, device := range devices {
		if device.ID == id {
			return true
		}
	}
	return false
}

type frameWriter struct {
	frames chan []byte
	done   chan struct{}
	cancel context.CancelFunc
	once   sync.Once
	mu     sync.Mutex
	err    error
}

func newFrameWriter(cancel context.CancelFunc) *frameWriter {
	return &frameWriter{frames: make(chan []byte, queueFrames), done: make(chan struct{}), cancel: cancel}
}

func (w *frameWriter) push(frame []byte) {
	for {
		select {
		case w.frames <- frame:
			return
		default:
		}
		select {
		case <-w.frames:
			w.once.Do(func() {
				fmt.Fprintln(os.Stderr, "Warning: audio output queue overflow. Captured audio was dropped.")
			})
		default:
		}
	}
}

func (w *frameWriter) run() {
	defer close(w.done)
	for frame := range w.frames {
		if _, err := os.Stdout.Write(frame); err != nil {
			w.mu.Lock()
			w.err = err
			w.mu.Unlock()
			w.cancel()
			return
		}
	}
}

func (w *frameWriter) failure() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.err
}

type stream struct {
	source byte
	device string
	label  string
}

func captureStream(ctx context.Context, s stream, start time.Time, out *frameWriter) error {
	cmd := exec.CommandContext(ctx, "parec",
		"--raw",
		fmt.Sprintf("--rate=%d", sampleRate),
		fmt.Sprintf("--channels=%d", channels),
		"--format=s16le",
		"--latency-msec=100",
		"--device="+s.device,
		"--client-name=Titen",
		"--stream-name=Titen capture")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("open %s capture: %w", s.label, err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("open %s diagnostics: %w", s.label, err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s capture: %w", s.label, err)
	}
	go func() {
		scanner := bufio.NewScanner(stderr)
		scanner.Buffer(make([]byte, 4096), 64<<10)
		for scanner.Scan() {
			if line := strings.TrimSpace(scanner.Text()); line != "" {
				fmt.Fprintln(os.Stderr, line)
			}
		}
	}()
	buf := make([]byte, frameBytes)
	var samples int64
	for {
		if _, err := io.ReadFull(stdout, buf); err != nil {
			if ctx.Err() != nil {
				_ = cmd.Wait()
				return nil
			}
			waitErr := cmd.Wait()
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				if waitErr != nil {
					return fmt.Errorf("%s capture stopped: %w", s.label, waitErr)
				}
				return fmt.Errorf("%s capture ended unexpectedly", s.label)
			}
			return fmt.Errorf("read %s capture: %w", s.label, err)
		}
		out.push(encodeFrame(s.source, time.Duration(samples)*time.Second/sampleRate, samples16(buf)))
		samples += frameSamples
	}
}

func samples16(pcm []byte) []int16 {
	samples := make([]int16, len(pcm)/2)
	for i := range samples {
		samples[i] = int16(binary.LittleEndian.Uint16(pcm[i*2:]))
	}
	return samples
}

func selfTest() error {
	frame := encodeFrame(micSource, 1500*time.Millisecond, []int16{1, -2, 32767, -32768})
	if string(frame[:4]) != "TIT1" || frame[4] != micSource || frame[5] != 1 {
		return errors.New("self-test: frame header is invalid")
	}
	if binary.LittleEndian.Uint16(frame[6:]) != channels || binary.LittleEndian.Uint32(frame[8:]) != sampleRate {
		return errors.New("self-test: frame format is invalid")
	}
	if binary.LittleEndian.Uint64(frame[12:]) != uint64(1500*time.Millisecond) {
		return errors.New("self-test: frame timestamp is invalid")
	}
	if binary.LittleEndian.Uint32(frame[20:]) != 8 || len(frame) != 32 {
		return errors.New("self-test: frame payload size is invalid")
	}
	decoded := samples16(frame[24:])
	if decoded[0] != 1 || decoded[1] != -2 || decoded[2] != 32767 || decoded[3] != -32768 {
		return errors.New("self-test: frame samples did not roundtrip")
	}
	devices, err := parseSources([]byte(`[
		{"name":"alsa_output.built-in.monitor","description":"Monitor of Built-in Audio","monitor_source":"alsa_output.built-in"},
		{"name":"alsa_input.built-in","description":"Built-in Microphone"},
		{"name":"alsa_input.aux"}
	]`))
	if err != nil {
		return fmt.Errorf("self-test: %w", err)
	}
	if len(devices) != 2 || devices[0].ID != "alsa_input.built-in" || devices[0].Name != "Built-in Microphone" ||
		devices[1].ID != "alsa_input.aux" || devices[1].Name != "alsa_input.aux" {
		return errors.New("self-test: source list did not skip monitors")
	}
	return nil
}

func run() error {
	fs := flag.NewFlagSet("titen-audio", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	microphoneOnly := fs.Bool("microphone-only", false, "capture the microphone")
	systemOnly := fs.Bool("system-only", false, "capture playback")
	microphone := fs.String("microphone", "", "microphone source name")
	list := fs.Bool("list-devices", false, "print microphone sources as JSON")
	test := fs.Bool("self-test", false, "check the frame format without audio hardware")
	if err := fs.Parse(os.Args[1:]); err != nil {
		return errors.New("Usage: titen-audio [--microphone-only | --system-only] [--microphone source] | --list-devices | --self-test")
	}
	if fs.NArg() != 0 {
		return errors.New("Usage: titen-audio [--microphone-only | --system-only] [--microphone source] | --list-devices | --self-test")
	}
	switch {
	case *test:
		if err := selfTest(); err != nil {
			return err
		}
		fmt.Println("titen-audio self-test passed")
		return nil
	case *list:
		return listDevices()
	case *microphoneOnly && *systemOnly:
		return errors.New("choose one capture mode")
	}

	if _, err := exec.LookPath("parec"); err != nil {
		return errors.New("parec is unavailable; install pulseaudio-utils")
	}
	if !*systemOnly && *microphone != "" {
		devices, err := listSources()
		if err != nil {
			return err
		}
		if !hasDevice(devices, *microphone) {
			return errors.New("the selected microphone is unavailable. Choose another microphone in Settings.")
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	streams := make([]stream, 0, 2)
	if !*systemOnly {
		source := *microphone
		if source == "" {
			source = "@DEFAULT_SOURCE@"
		}
		streams = append(streams, stream{source: micSource, device: source, label: "microphone"})
	}
	if !*microphoneOnly {
		streams = append(streams, stream{source: systemSource, device: "@DEFAULT_MONITOR@", label: "system audio"})
	}
	out := newFrameWriter(cancel)
	go out.run()
	go func() {
		_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
		cancel()
	}()
	start := time.Now()
	errs := make(chan error, len(streams))
	var wg sync.WaitGroup
	for _, s := range streams {
		wg.Add(1)
		go func(s stream) {
			defer wg.Done()
			if err := captureStream(ctx, s, start, out); err != nil {
				errs <- err
				cancel()
			}
		}(s)
	}
	wg.Wait()
	close(errs)
	close(out.frames)
	<-out.done
	if err := out.failure(); err != nil {
		return fmt.Errorf("write captured audio: %w", err)
	}
	for err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
