package platform

import (
	"os"
	"path/filepath"
)

func HelperPath(targetOS, executable, projectDir string) string {
	name := "titen-audio"
	if targetOS == "windows" {
		name += ".exe"
	}
	packaged := filepath.Join(filepath.Dir(executable), name)
	if info, err := os.Stat(packaged); err == nil && !info.IsDir() {
		return packaged
	}
	var candidates []string
	switch targetOS {
	case "darwin":
		candidates = []string{filepath.Join(projectDir, "native", "macos", ".build", "release", name)}
	case "windows":
		candidates = []string{filepath.Join(projectDir, "build", "windows-audio", "Release", name), filepath.Join(projectDir, "build", "windows-audio", name)}
	case "linux":
		candidates = []string{filepath.Join(projectDir, "build", "linux", name)}
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return packaged
}

func CredentialStore(targetOS string) string {
	switch targetOS {
	case "darwin":
		return "macOS Keychain"
	case "windows":
		return "Windows Credential Manager"
	case "linux":
		return "the desktop Secret Service"
	default:
		return "the OS credential store"
	}
}
