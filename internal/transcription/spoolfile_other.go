//go:build !windows

package transcription

import "os"

func createSpoolFile() (*os.File, error) {
	file, err := os.CreateTemp("", "titen-audio-*")
	if err != nil {
		return nil, err
	}
	if err = os.Remove(file.Name()); err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}
