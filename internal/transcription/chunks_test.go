package transcription

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func TestRecordingIsReadableBeforeClose(t *testing.T) {
	dir := t.TempDir()
	r, err := NewRecorder(dir, "meeting", 0, func(ChunkMeta) {}, func(err error) { t.Error(err) }, func() string { return "chunk" })
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	r.Write(make([]byte, 32000))
	data, err := os.ReadFile(filepath.Join(dir, "meeting-001.wav"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data[:4]) != "RIFF" || binary.LittleEndian.Uint32(data[40:44]) != 32000 {
		t.Fatal("invalid live WAV header")
	}
}
