package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"debug/macho"
	"debug/pe"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func check(directory, target, arch string) error {
	var suffix string
	var executables []string
	switch target {
	case "macOS":
		suffix = "macos"
		executables = []string{"Titen.app/Contents/MacOS/titen", "Titen.app/Contents/MacOS/titen-audio"}
	case "Windows":
		suffix = "windows"
		executables = []string{"Titen/Titen.exe", "Titen/titen-audio.exe"}
	default:
		return fmt.Errorf("unsupported package target %q", target)
	}
	archives, err := filepath.Glob(filepath.Join(directory, "Titen-*-"+suffix+"-"+arch+".zip"))
	if err != nil {
		return err
	}
	if len(archives) != 1 {
		return fmt.Errorf("expected one %s/%s archive, found %d", target, arch, len(archives))
	}
	archive := archives[0]
	data, err := os.ReadFile(archive)
	if err != nil {
		return err
	}
	checksum, err := os.ReadFile(archive + ".sha256")
	if err != nil {
		return err
	}
	fields := strings.Fields(string(checksum))
	digest := fmt.Sprintf("%x", sha256.Sum256(data))
	if len(fields) != 2 || fields[0] != digest || fields[1] != filepath.Base(archive) {
		return fmt.Errorf("checksum does not match %s", archive)
	}
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return err
	}
	entries := make(map[string]*zip.File)
	for _, entry := range reader.File {
		if _, exists := entries[entry.Name]; exists {
			return fmt.Errorf("duplicate archive entry %s", entry.Name)
		}
		entries[entry.Name] = entry
	}
	if target == "macOS" && entries["Titen.app/Contents/Info.plist"] == nil {
		return fmt.Errorf("app Info.plist is missing")
	}
	for _, name := range executables {
		if entries[name] == nil {
			return fmt.Errorf("package is missing %s", name)
		}
	}
	for _, name := range executables {
		entry := entries[name]
		if entry == nil {
			return fmt.Errorf("package is missing %s", name)
		}
		if entry.UncompressedSize64 > 128<<20 {
			return fmt.Errorf("binary exceeds validation limit")
		}
		stream, err := entry.Open()
		if err != nil {
			return err
		}
		binary, err := io.ReadAll(io.LimitReader(stream, 128<<20))
		stream.Close()
		if err != nil {
			return err
		}
		if target == "macOS" {
			if entry.Mode().Perm()&0111 == 0 {
				return fmt.Errorf("%s lost executable permissions", name)
			}
			file, err := macho.NewFile(bytes.NewReader(binary))
			if err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			want := macho.CpuArm64
			if arch == "amd64" {
				want = macho.CpuAmd64
			} else if arch != "arm64" {
				return fmt.Errorf("unsupported macOS architecture %s", arch)
			}
			if file.Cpu != want {
				return fmt.Errorf("%s has CPU %s instead of %s", name, file.Cpu, want)
			}
		} else {
			file, err := pe.NewFile(bytes.NewReader(binary))
			if err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			if arch != "amd64" || file.Machine != pe.IMAGE_FILE_MACHINE_AMD64 {
				return fmt.Errorf("%s is not Windows amd64", name)
			}
		}
	}
	fmt.Printf("Verified %s with native %s/%s app, capture helper, and matching SHA256\n", archive, target, arch)
	return nil
}
func main() {
	directory := flag.String("dir", "dist", "archive directory")
	target := flag.String("os", "", "runner OS")
	arch := flag.String("arch", "", "target architecture")
	flag.Parse()
	if err := check(*directory, *target, *arch); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
