package transcription

import (
	"bytes"
	"context"
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
