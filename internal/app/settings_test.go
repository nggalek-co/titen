package app

import (
	"os"
	"path/filepath"
	"testing"
	"titen/internal/saas"
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

func TestProductionDefaultMigratesImplicitLocalhostAndPreservesCustomLocalhost(t *testing.T) {
	dir := t.TempDir()
	settings, err := LoadSettingsWithDefaultEndpoint(dir, saas.ProductionEndpoint)
	if err != nil || settings.ServerEndpoint != saas.ProductionEndpoint {
		t.Fatalf("production default = %+v, %v", settings, err)
	}

	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte(`{"server_endpoint":"http://localhost:3000"}`), 0600); err != nil {
		t.Fatal(err)
	}
	settings, err = LoadSettingsWithDefaultEndpoint(dir, saas.ProductionEndpoint)
	if err != nil || settings.ServerEndpoint != saas.ProductionEndpoint {
		t.Fatalf("legacy endpoint = %+v, %v", settings, err)
	}

	if err := os.WriteFile(path, []byte(`{"server_endpoint":"http://localhost:3000","server_endpoint_custom":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	settings, err = LoadSettingsWithDefaultEndpoint(dir, saas.ProductionEndpoint)
	if err != nil || settings.ServerEndpoint != saas.DefaultEndpoint {
		t.Fatalf("custom endpoint = %+v, %v", settings, err)
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
