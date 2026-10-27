//go:build linux

package main

import (
	"bytes"
	"testing"
	"time"

	"titen/internal/audio"
)

func TestEncodeFrameMatchesCaptureProtocol(t *testing.T) {
	frame, err := audio.ReadFrame(bytes.NewReader(encodeFrame(systemSource, 2*time.Second, []int16{7, -8, 9})))
	if err != nil {
		t.Fatal(err)
	}
	if frame.Source != audio.System || frame.Timestamp != 2*time.Second || frame.SampleRate != sampleRate || frame.Channels != channels {
		t.Fatalf("unexpected frame %+v", frame)
	}
	if len(frame.Samples) != 3 || frame.Samples[0] != 7 || frame.Samples[1] != -8 || frame.Samples[2] != 9 {
		t.Fatalf("unexpected samples %v", frame.Samples)
	}
}

func TestParseSourcesSkipsMonitorsAndFallsBackToName(t *testing.T) {
	devices, err := parseSources([]byte(`[
		{"name":"alsa_output.hdmi.monitor","description":"Monitor of HDMI","monitor_source":"alsa_output.hdmi"},
		{"name":"alsa_input.usb","description":"USB Microphone","monitor_source":""},
		{"name":"alsa_input.aux"}
	]`))
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 2 {
		t.Fatalf("unexpected devices %+v", devices)
	}
	if devices[0] != (device{ID: "alsa_input.usb", Name: "USB Microphone"}) {
		t.Fatalf("unexpected first device %+v", devices[0])
	}
	if devices[1] != (device{ID: "alsa_input.aux", Name: "alsa_input.aux"}) {
		t.Fatalf("unexpected fallback device %+v", devices[1])
	}
}

func TestHasDevice(t *testing.T) {
	devices := []device{{ID: "alsa_input.usb"}, {ID: "alsa_input.aux"}}
	if !hasDevice(devices, "alsa_input.aux") || hasDevice(devices, "alsa_input.missing") {
		t.Fatal("device lookup did not match the source list")
	}
}

func TestSelfTest(t *testing.T) {
	if err := selfTest(); err != nil {
		t.Fatal(err)
	}
}
