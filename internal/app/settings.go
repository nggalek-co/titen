package app

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"titen/internal/saas"
)

type Settings struct {
	ServerEndpoint string `json:"server_endpoint"`
	MicrophoneID   string `json:"microphone_id"`
	CaptureMode    string `json:"capture_mode"`
	HelperPath     string `json:"helper_path"`
	Language       string `json:"language"`
}

func LoadSettings(dir string) (Settings, error) {
	s := Settings{ServerEndpoint: saas.DefaultEndpoint, CaptureMode: "Both", Language: "system"}
	b, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if err = json.Unmarshal(b, &s); err != nil {
		return s, err
	}
	s.ServerEndpoint, err = saas.NormalizeEndpoint(s.ServerEndpoint)
	return s, err
}

func SaveSettings(dir string, s Settings) error {
	endpoint, err := saas.NormalizeEndpoint(s.ServerEndpoint)
	if err != nil {
		return err
	}
	s.ServerEndpoint = endpoint
	if s.CaptureMode != "" && s.CaptureMode != "Both" && s.CaptureMode != "Microphone only" && s.CaptureMode != "System audio only" {
		return errors.New("select a supported audio capture mode")
	}
	if s.Language != "" && s.Language != "system" && s.Language != "en" && s.Language != "id" {
		return errors.New("select a supported language")
	}
	if s.Language == "" {
		s.Language = "system"
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
