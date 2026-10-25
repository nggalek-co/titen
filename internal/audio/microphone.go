package audio

import (
	"context"
	"errors"
	"log/slog"
)

type MicrophoneResult struct {
	Frames int
	Peak   float64
}

func TestMicrophone(ctx context.Context, cfg CaptureConfig, onLevel func(float64)) (MicrophoneResult, error) {
	cfg.Mode = "Microphone only"
	capture, err := StartCapture(ctx, cfg)
	if err != nil {
		return MicrophoneResult{}, err
	}
	defer capture.Close()
	var result MicrophoneResult
	finish := func() (MicrophoneResult, error) {
		if result.Frames == 0 {
			return result, errors.New("no microphone samples received; check permission and the selected device")
		}
		return result, nil
	}
	diagnostics := capture.Errors
	warnings := capture.Warnings
	for {
		select {
		case <-ctx.Done():
			return finish()
		case err, ok := <-diagnostics:
			if !ok {
				diagnostics = nil
				continue
			}
			if err != nil {
				return result, err
			}
		case warning, ok := <-warnings:
			if !ok {
				warnings = nil
				continue
			}
			if warning != nil {
				slog.Warn("microphone test helper warning", "warning", warning)
			}
		case frame, ok := <-capture.Frames:
			if !ok {
				if diagnostics != nil {
					for err := range diagnostics {
						if err != nil {
							return result, err
						}
					}
				}
				if warnings != nil {
					for warning := range warnings {
						if warning != nil {
							slog.Warn("microphone test helper warning", "warning", warning)
						}
					}
				}
				return result, errors.New("microphone capture ended before the test finished")
			}
			if frame.Source != Microphone {
				continue
			}
			level := Level(Normalize(frame))
			result.Frames++
			if level > result.Peak {
				result.Peak = level
			}
			if onLevel != nil {
				onLevel(level)
			}
		}
	}
}
