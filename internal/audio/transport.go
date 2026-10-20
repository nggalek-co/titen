package audio

import (
	"encoding/binary"
	"errors"
	"io"
	"time"
)

type Source uint8

const (
	Microphone Source = 1
	System     Source = 2
)
const SampleRate = 16000
const maxPayload = 192000

type Frame struct {
	Source     Source
	Timestamp  time.Duration
	SampleRate int
	Channels   int
	Samples    []int16
}

// The wire header is fixed-size and little endian. Payloads are signed PCM16.
func ReadFrame(r io.Reader) (Frame, error) {
	var h [24]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return Frame{}, err
	}
	if string(h[:4]) != "TIT1" || h[5] != 1 {
		return Frame{}, errors.New("unsupported audio protocol")
	}
	f := Frame{Source: Source(h[4]), Channels: int(binary.LittleEndian.Uint16(h[6:8])), SampleRate: int(binary.LittleEndian.Uint32(h[8:12])), Timestamp: time.Duration(binary.LittleEndian.Uint64(h[12:20]))}
	n := int(binary.LittleEndian.Uint32(h[20:24]))
	if (f.Source != Microphone && f.Source != System) || f.Channels < 1 || f.Channels > 8 || f.SampleRate < 8000 || f.SampleRate > 192000 || n <= 0 || n > maxPayload || n%(2*f.Channels) != 0 || f.Timestamp < 0 {
		return Frame{}, errors.New("invalid audio frame")
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return Frame{}, err
	}
	f.Samples = make([]int16, n/2)
	for i := range f.Samples {
		f.Samples[i] = int16(binary.LittleEndian.Uint16(b[i*2:]))
	}
	return f, nil
}
