package audio

import (
	"context"
	"encoding/binary"
	"math"
	"time"
)

type Chunk struct {
	Timestamp                    time.Duration
	PCM                          []byte
	MicrophoneLevel, SystemLevel float64
}
type Pipeline struct{}

// Normalize averages channels before linearly resampling to the provider rate.
func Normalize(f Frame) []int16 {
	n := len(f.Samples) / f.Channels
	if n == 0 {
		return nil
	}
	mono := make([]float64, n)
	for i := range mono {
		for c := 0; c < f.Channels; c++ {
			mono[i] += float64(f.Samples[i*f.Channels+c]) / float64(f.Channels)
		}
	}
	count := int(math.Round(float64(n) * SampleRate / float64(f.SampleRate)))
	out := make([]int16, count)
	for i := range out {
		p := float64(i) * float64(f.SampleRate) / SampleRate
		a := int(p)
		if a >= n {
			a = n - 1
		}
		b := min(a+1, n-1)
		out[i] = int16(math.Round(mono[a] + (mono[b]-mono[a])*(p-float64(a))))
	}
	return out
}
func Level(s []int16) float64 {
	if len(s) == 0 {
		return 0
	}
	var v float64
	for _, x := range s {
		v += float64(x) * float64(x)
	}
	return math.Sqrt(v/float64(len(s))) / 32768
}

// Run aligns a bounded 100ms window on the shared capture clock. Missing input
// contributes silence so an inactive microphone never stalls system audio.
func (Pipeline) Run(ctx context.Context, frames <-chan Frame, emit func(Chunk) error) error {
	const block = 320
	const horizon = 1600
	type samples struct{ mic, sys map[int64]int16 }
	q := samples{make(map[int64]int16), make(map[int64]int16)}
	var cursor, latest int64
	started := false
	flush := func(until int64) error {
		for cursor+block <= until {
			m, s := make([]int16, block), make([]int16, block)
			pcm := make([]byte, block*2)
			for i := 0; i < block; i++ {
				k := cursor + int64(i)
				m[i] = q.mic[k]
				s[i] = q.sys[k]
				delete(q.mic, k)
				delete(q.sys, k)
				mixed := int32(m[i])/2 + int32(s[i])/2
				binary.LittleEndian.PutUint16(pcm[i*2:], uint16(int16(mixed)))
			}
			if err := emit(Chunk{time.Duration(cursor/SampleRate)*time.Second + time.Duration(cursor%SampleRate)*time.Second/SampleRate, pcm, Level(m), Level(s)}); err != nil {
				return err
			}
			cursor += block
		}
		return nil
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case f, ok := <-frames:
			if !ok {
				return flush(latest + block - 1)
			}
			normalized := Normalize(f)
			start := int64(f.Timestamp/time.Second)*SampleRate + int64(f.Timestamp%time.Second)*SampleRate/int64(time.Second)
			if !started {
				cursor = start
				latest = start
				started = true
			}
			if start > latest+SampleRate {
				if err := flush(latest + block - 1); err != nil {
					return err
				}
				cursor = start
			}
			if end := start + int64(len(normalized)); end > latest {
				latest = end
			}
			target := q.mic
			if f.Source == System {
				target = q.sys
			}
			for i, v := range normalized {
				k := start + int64(i)
				if k >= cursor {
					target[k] = v
				}
			}
			if err := flush(latest - horizon); err != nil {
				return err
			}
		}
	}
}
