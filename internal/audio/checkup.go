package audio

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// CheckPermissions inspects access without starting capture. ScreenCaptureKit may
// show a macOS consent prompt when system audio access has not been granted.
func CheckPermissions(ctx context.Context, cfg CaptureConfig) error {
	return checkPermissions(ctx, cfg, false)
}

// RequestPermissions asks macOS for consent after an explicit user action.
func RequestPermissions(ctx context.Context, cfg CaptureConfig) error {
	return checkPermissions(ctx, cfg, true)
}

func checkPermissions(ctx context.Context, cfg CaptureConfig, request bool) error {
	if _, err := os.Stat(cfg.HelperPath); err != nil {
		return fmt.Errorf("audio helper unavailable: %w", err)
	}
	if runtime.GOOS != "darwin" {
		return nil
	}
	argument := "--check-permissions"
	if request {
		argument = "--request-permissions"
	}
	args := []string{argument}
	if cfg.Mode == "Microphone only" {
		args = append(args, "--microphone-only")
	} else if cfg.Mode == "System audio only" {
		args = append(args, "--system-only")
	}
	cmd := exec.CommandContext(ctx, cfg.HelperPath, args...)
	configureHelperCommand(cmd)
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("audio permission check failed: %w", err)
	}
	var permissions struct {
		Microphone        bool   `json:"microphone"`
		SystemAudio       bool   `json:"systemAudio"`
		MicrophoneStatus  string `json:"microphoneStatus"`
		SystemAudioStatus string `json:"systemAudioStatus"`
		SystemAudioError  string `json:"systemAudioError"`
	}
	if err := json.Unmarshal(out, &permissions); err != nil {
		return fmt.Errorf("invalid audio permission response: %w", err)
	}
	slog.Info("audio permissions checked", "requested", request, "mode", cfg.Mode, "microphone", permissions.MicrophoneStatus, "system_audio", permissions.SystemAudioStatus, "system_audio_error", permissions.SystemAudioError)
	var missing []string
	if cfg.Mode != "System audio only" && !permissions.Microphone {
		if permissions.MicrophoneStatus == "notDetermined" {
			missing = append(missing, "Microphone (access not requested yet)")
		} else {
			missing = append(missing, "Microphone")
		}
	}
	if cfg.Mode != "Microphone only" && !permissions.SystemAudio {
		missing = append(missing, "Screen & System Audio Recording")
	}
	if len(missing) > 0 {
		return fmt.Errorf("Titen cannot access %s. Request access or enable this Titen build in System Settings > Privacy & Security. If already enabled, restart this build and recheck. Rebuilt apps may need permission approval again", strings.Join(missing, " and "))
	}
	return nil
}
