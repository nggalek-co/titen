package transcription

import (
	"os"
	"syscall"
)

const (
	fileFlagDeleteOnClose  = 0x04000000
	fileAttributeTemporary = 0x00000100
)

func createSpoolFile() (*os.File, error) {
	seed, err := os.CreateTemp("", "titen-audio-*")
	if err != nil {
		return nil, err
	}
	name := seed.Name()
	if err = seed.Close(); err != nil {
		os.Remove(name)
		return nil, err
	}
	path, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		os.Remove(name)
		return nil, err
	}
	handle, err := syscall.CreateFile(path, syscall.GENERIC_READ|syscall.GENERIC_WRITE, syscall.FILE_SHARE_DELETE, nil, syscall.OPEN_EXISTING, fileAttributeTemporary|fileFlagDeleteOnClose, 0)
	if err != nil {
		os.Remove(name)
		return nil, err
	}
	file := os.NewFile(uintptr(handle), name)
	return file, nil
}
