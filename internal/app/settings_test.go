package app

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSettingsDefaultAndEndpointPersistence(t *testing.T) {
	dir := t.TempDir()
	settings, err := LoadSettings(dir)
	if err != nil {
		t.Fatal(err)
	}
	if settings.ServerEndpoint != "http://localhost:3000" {
		t.Fatalf("default endpoint = %q", settings.ServerEndpoint)
	}
	settings.ServerEndpoint = "https://Titen.example:443/"
	if err = SaveSettings(dir, settings); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadSettings(dir)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ServerEndpoint != "https://titen.example" {
		t.Fatalf("loaded endpoint = %q", loaded.ServerEndpoint)
	}
}

func TestSettingsRejectPublicHTTPAndKeepsLegacySettingsLocalhost(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"capture_mode":"Both"}`), 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadSettings(dir)
	if err != nil || loaded.ServerEndpoint != "http://localhost:3000" {
		t.Fatalf("legacy settings = %+v, %v", loaded, err)
	}
	loaded.ServerEndpoint = "http://example.com"
	if err = SaveSettings(dir, loaded); err == nil {
		t.Fatal("accepted public HTTP endpoint")
	}
}
