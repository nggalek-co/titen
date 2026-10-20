package app

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

type Settings struct {
	MicrophoneID   string `json:"microphone_id"`
	CaptureMode    string `json:"capture_mode"`
	Languages      string `json:"languages"`
	GoogleClientID string `json:"google_client_id"`
	AutoSync       bool   `json:"auto_sync"`
	HelperPath     string `json:"helper_path"`
}

func LoadSettings(dir string) (Settings, error) {
	s := Settings{Languages: "id,en", AutoSync: true, CaptureMode: "Both"}
	b, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	err = json.Unmarshal(b, &s)
	return s, err
}

func SaveSettings(dir string, s Settings) error {
	if s.CaptureMode != "" && s.CaptureMode != "Both" && s.CaptureMode != "Microphone only" && s.CaptureMode != "System audio only" {
		return errors.New("select a supported audio capture mode")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(dir, "settings.json")
	if err = os.WriteFile(path+".tmp", b, 0600); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}
