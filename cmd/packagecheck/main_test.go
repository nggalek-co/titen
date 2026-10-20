package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRejectPackageMissingCaptureHelper(t *testing.T) {
	var data bytes.Buffer
	writer := zip.NewWriter(&data)
	entry, _ := writer.Create("Titen/Titen.exe")
	entry.Write([]byte("not a binary"))
	writer.Close()
	dir := t.TempDir()
	name := "Titen-dev-windows-amd64.zip"
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".sha256", []byte(fmt.Sprintf("%x  %s\n", sha256.Sum256(data.Bytes()), name)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := check(dir, "Windows", "amd64"); err == nil || !strings.Contains(err.Error(), "titen-audio.exe") {
		t.Fatal("incomplete or invalid binary package accepted")
	}
	if err := os.WriteFile(path+".sha256", []byte("wrong hash"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := check(dir, "Windows", "amd64"); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("modified checksum accepted: %v", err)
	}
}
