package transcription

import (
	"bytes"
	"context"
	"os"
	"testing"
)

func TestAudioSpoolPreservesOrder(t *testing.T) {
	input := make(chan []byte, 3)
	for _, data := range [][]byte{[]byte("one"), []byte("two"), []byte("three")} {
		input <- data
	}
	close(input)
	out, cleanup, err := spool(context.Background(), input, func(err error) { t.Error(err) })
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	var got bytes.Buffer
	for frame := range out {
		got.Write(frame)
	}
	if got.String() != "onetwothree" {
		t.Fatalf("got %q", got.String())
	}
}

func TestSpoolFileDeletedOnClose(t *testing.T) {
	file, err := createSpoolFile()
	if err != nil {
		t.Fatal(err)
	}
	name := file.Name()
	defer file.Close()
	if _, err = file.WriteAt([]byte("test"), 0); err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 4)
	if _, err = file.ReadAt(data, 0); err != nil || string(data) != "test" {
		t.Fatalf("spool IO %q %v", data, err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(name); !os.IsNotExist(err) {
		t.Fatalf("spool file survived close: %v", err)
	}
}
