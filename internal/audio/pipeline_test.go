package audio

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"math"
	"testing"
	"time"
)

func wire(source byte, rate uint32, channels uint16, samples []int16) []byte {
	b := make([]byte, 24+len(samples)*2)
	copy(b, "TIT1")
	b[4] = source
	b[5] = 1
	binary.LittleEndian.PutUint16(b[6:], channels)
	binary.LittleEndian.PutUint32(b[8:], rate)
	binary.LittleEndian.PutUint64(b[12:], uint64(time.Second))
	binary.LittleEndian.PutUint32(b[20:], uint32(len(samples)*2))
	for i, x := range samples {
		binary.LittleEndian.PutUint16(b[24+i*2:], uint16(x))
	}
	return b
}
func TestReadFrame(t *testing.T) {
	b := wire(1, 16000, 1, []int16{-32768, 32767})
	f, e := ReadFrame(bytes.NewReader(b))
	if e != nil || f.Samples[0] != -32768 || f.Timestamp != time.Second {
		t.Fatalf("%+v %v", f, e)
	}
	for _, bad := range [][]byte{b[:10], append([]byte("NOPE"), b[4:]...), wire(9, 16000, 1, []int16{1}), wire(1, 16000, 0, []int16{1})} {
		if _, e := ReadFrame(bytes.NewReader(bad)); e == nil {
			t.Fatal("invalid frame accepted")
		}
	}
	if _, e := ReadFrame(bytes.NewReader(b[:len(b)-1])); e != io.ErrUnexpectedEOF {
		t.Fatalf("truncated PCM: %v", e)
	}
}
func TestNormalizeStereoAndResample(t *testing.T) {
	s := []int16{100, 300, 300, 500, 500, 700, 700, 900}
	got := Normalize(Frame{Channels: 2, SampleRate: 32000, Samples: s})
	if len(got) != 2 || got[0] != 200 || got[1] != 600 {
		t.Fatal(got)
	}
	if math.Abs(Level([]int16{16384, -16384})-.5) > .0001 {
		t.Fatal("level")
	}
}
func TestTimestampAlignmentAndClipping(t *testing.T) {
	frames := make(chan Frame, 2)
	a := make([]int16, 320)
	b := make([]int16, 320)
	for i := range a {
		a[i] = 30000
		b[i] = 30000
	}
	frames <- Frame{Microphone, time.Second, 16000, 1, a}
	frames <- Frame{System, time.Second + 10*time.Millisecond, 16000, 1, b}
	close(frames)
	var chunks []Chunk
	err := (Pipeline{}).Run(context.Background(), frames, func(c Chunk) error { chunks = append(chunks, c); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 2 {
		t.Fatalf("got %d chunks", len(chunks))
	}
	sample := func(c, i int) int16 { return int16(binary.LittleEndian.Uint16(chunks[c].PCM[i*2:])) }
	if sample(0, 0) != 15000 || sample(0, 200) != 30000 || sample(1, 0) != 15000 {
		t.Fatal("timestamps not aligned")
	}
	if chunks[0].Timestamp != time.Second {
		t.Fatal(chunks[0].Timestamp)
	}
}
func TestLargeTimestamp(t *testing.T) {
	frames := make(chan Frame, 1)
	frames <- Frame{System, 300 * time.Hour, 16000, 1, make([]int16, 320)}
	close(frames)
	err := (Pipeline{}).Run(context.Background(), frames, func(c Chunk) error {
		if c.Timestamp != 300*time.Hour {
			t.Fatal(c.Timestamp)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
