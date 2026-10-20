package platform

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHelperPaths(t *testing.T) {
	for _, target := range []string{"darwin", "windows"} {
		t.Run(target, func(t *testing.T) {
			root := t.TempDir()
			exe := filepath.Join(root, "app", "titen")
			name := "titen-audio"
			if target == "windows" {
				name += ".exe"
			}
			packaged := filepath.Join(filepath.Dir(exe), name)
			if got := HelperPath(target, exe, root); got != packaged {
				t.Fatalf("missing helper path %s", got)
			}
			var dev string
			if target == "darwin" {
				dev = filepath.Join(root, "native", "macos", ".build", "release", name)
			} else {
				dev = filepath.Join(root, "build", "windows-audio", "Release", name)
			}
			if err := os.MkdirAll(filepath.Dir(dev), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(dev, nil, 0700); err != nil {
				t.Fatal(err)
			}
			if got := HelperPath(target, exe, root); got != dev {
				t.Fatalf("development helper %s", got)
			}
			if err := os.MkdirAll(filepath.Dir(packaged), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(packaged, nil, 0700); err != nil {
				t.Fatal(err)
			}
			if got := HelperPath(target, exe, root); got != packaged {
				t.Fatalf("packaged helper must take precedence, got %s", got)
			}
		})
	}
}
