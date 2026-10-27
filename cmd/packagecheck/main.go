package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"debug/elf"
	"debug/macho"
	"debug/pe"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const maxBinary = 128 << 20

type entry struct {
	data []byte
	mode os.FileMode
}

func readZip(data []byte) (map[string]entry, error) {
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	entries := make(map[string]entry)
	for _, file := range reader.File {
		if _, exists := entries[file.Name]; exists {
			return nil, fmt.Errorf("duplicate archive entry %s", file.Name)
		}
		if file.UncompressedSize64 > maxBinary {
			return nil, fmt.Errorf("%s exceeds validation limit", file.Name)
		}
		stream, err := file.Open()
		if err != nil {
			return nil, err
		}
		content, err := io.ReadAll(io.LimitReader(stream, maxBinary))
		stream.Close()
		if err != nil {
			return nil, err
		}
		entries[file.Name] = entry{data: content, mode: file.Mode()}
	}
	return entries, nil
}

func readTarGz(data []byte) (map[string]entry, error) {
	reader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	entries := make(map[string]entry)
	archive := tar.NewReader(reader)
	for {
		header, err := archive.Next()
		if err == io.EOF {
			return entries, nil
		}
		if err != nil {
			return nil, err
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}
		if _, exists := entries[header.Name]; exists {
			return nil, fmt.Errorf("duplicate archive entry %s", header.Name)
		}
		if header.Size > maxBinary {
			return nil, fmt.Errorf("%s exceeds validation limit", header.Name)
		}
		content, err := io.ReadAll(io.LimitReader(archive, maxBinary))
		if err != nil {
			return nil, err
		}
		entries[header.Name] = entry{data: content, mode: os.FileMode(header.Mode)}
	}
}

func checkBinary(target, arch, name string, e entry) error {
	if target != "Windows" && e.mode.Perm()&0111 == 0 {
		return fmt.Errorf("%s lost executable permissions", name)
	}
	switch target {
	case "macOS":
		file, err := macho.NewFile(bytes.NewReader(e.data))
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
	case "Windows":
		file, err := pe.NewFile(bytes.NewReader(e.data))
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if arch != "amd64" || file.Machine != pe.IMAGE_FILE_MACHINE_AMD64 {
			return fmt.Errorf("%s is not Windows amd64", name)
		}
	case "Linux":
		file, err := elf.NewFile(bytes.NewReader(e.data))
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		want := elf.EM_X86_64
		if arch == "arm64" {
			want = elf.EM_AARCH64
		} else if arch != "amd64" {
			return fmt.Errorf("unsupported Linux architecture %s", arch)
		}
		if file.Machine != want {
			return fmt.Errorf("%s has machine %s instead of %s", name, file.Machine, want)
		}
	}
	return nil
}

func readAr(data []byte) (map[string][]byte, error) {
	if len(data) < 8 || string(data[:8]) != "!<arch>\n" {
		return nil, errors.New("not a Debian package")
	}
	entries := make(map[string][]byte)
	for offset := 8; offset+60 <= len(data); {
		header := data[offset : offset+60]
		if string(header[58:60]) != "`\n" {
			return nil, errors.New("invalid Debian package entry")
		}
		name := strings.TrimSuffix(strings.TrimSpace(string(header[:16])), "/")
		size, err := strconv.Atoi(strings.TrimSpace(string(header[48:58])))
		if err != nil || size < 0 {
			return nil, fmt.Errorf("invalid Debian package entry %q", name)
		}
		start := offset + 60
		end := start + size
		if end > len(data) {
			return nil, fmt.Errorf("truncated Debian package entry %q", name)
		}
		entries[name] = data[start:end]
		offset = end
		if size%2 == 1 {
			offset++
		}
	}
	return entries, nil
}

func verifyChecksum(path string, data, checksum []byte) error {
	fields := strings.Fields(string(checksum))
	digest := fmt.Sprintf("%x", sha256.Sum256(data))
	if len(fields) != 2 || fields[0] != digest || fields[1] != filepath.Base(path) {
		return fmt.Errorf("checksum does not match %s", path)
	}
	return nil
}

func checkDeb(directory, arch string) error {
	debs, err := filepath.Glob(filepath.Join(directory, "linux-"+arch, "titen_*_"+arch+".deb"))
	if err != nil {
		return err
	}
	if len(debs) != 1 {
		return fmt.Errorf("expected one Linux/%s Debian package, found %d", arch, len(debs))
	}
	deb := debs[0]
	data, err := os.ReadFile(deb)
	if err != nil {
		return err
	}
	checksum, err := os.ReadFile(deb + ".sha256")
	if err != nil {
		return err
	}
	if err := verifyChecksum(deb, data, checksum); err != nil {
		return err
	}
	archive, err := readAr(data)
	if err != nil {
		return err
	}
	controlArchive, err := readTarGz(archive["control.tar.gz"])
	if err != nil {
		return fmt.Errorf("read Debian control archive: %w", err)
	}
	controlEntry, ok := controlArchive["./control"]
	if !ok {
		controlEntry, ok = controlArchive["control"]
	}
	if !ok {
		return fmt.Errorf("Debian package has no control file")
	}
	control := string(controlEntry.data)
	for _, want := range []string{"Package: titen\n", "Architecture: " + arch + "\n", "pulseaudio-utils", "libsecret-1-0"} {
		if !strings.Contains(control, want) {
			return fmt.Errorf("Debian package control is missing %q", strings.TrimSpace(want))
		}
	}
	if strings.Contains(control, "libwebkit2gtk") {
		return fmt.Errorf("Debian package declares an unused WebKitGTK dependency")
	}
	dataArchive, err := readTarGz(archive["data.tar.gz"])
	if err != nil {
		return fmt.Errorf("read Debian data archive: %w", err)
	}
	for _, name := range []string{"./opt/titen/titen", "./opt/titen/titen-audio"} {
		e, ok := dataArchive[name]
		if !ok {
			return fmt.Errorf("Debian package is missing %s", name)
		}
		if err := checkBinary("Linux", arch, name, e); err != nil {
			return err
		}
	}
	if _, ok := dataArchive["./usr/share/applications/titen.desktop"]; !ok {
		return fmt.Errorf("Debian package is missing the desktop entry")
	}
	fmt.Printf("Verified %s with app, helper, desktop entry, and matching SHA256\n", deb)
	return nil
}

func check(directory, target, arch string) error {
	var suffix, pattern string
	var executables []string
	switch target {
	case "macOS":
		suffix = "macos"
		executables = []string{"Titen.app/Contents/MacOS/titen", "Titen.app/Contents/MacOS/titen-audio"}
	case "Windows":
		suffix = "windows"
		executables = []string{"Titen/Titen.exe", "Titen/titen-audio.exe"}
	case "Linux":
		suffix = "linux"
		executables = []string{"Titen/titen", "Titen/titen-audio"}
	default:
		return fmt.Errorf("unsupported package target %q", target)
	}
	pattern = "Titen-*-" + suffix + "-" + arch + ".zip"
	if target == "Linux" {
		pattern = "Titen-*-" + suffix + "-" + arch + ".tar.gz"
	}
	archives, err := filepath.Glob(filepath.Join(directory, pattern))
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
	if err := verifyChecksum(archive, data, checksum); err != nil {
		return err
	}
	var entries map[string]entry
	if target == "Linux" {
		entries, err = readTarGz(data)
	} else {
		entries, err = readZip(data)
	}
	if err != nil {
		return err
	}
	if target == "macOS" {
		if _, ok := entries["Titen.app/Contents/Info.plist"]; !ok {
			return fmt.Errorf("app Info.plist is missing")
		}
	}
	for _, name := range executables {
		if _, ok := entries[name]; !ok {
			return fmt.Errorf("package is missing %s", name)
		}
	}
	for _, name := range executables {
		e := entries[name]
		if err := checkBinary(target, arch, name, e); err != nil {
			return err
		}
	}
	if target == "Linux" {
		if err := checkDeb(directory, arch); err != nil {
			return err
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
