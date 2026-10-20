package transcription

import (
	"context"
	"fmt"
	"os"
	"sync"
)

func spool(ctx context.Context, audio <-chan []byte, failed func(error)) (<-chan []byte, func(), error) {
	file, err := os.CreateTemp("", "titen-audio-*")
	if err != nil {
		return nil, nil, err
	}
	if err = os.Remove(file.Name()); err != nil {
		file.Close()
		return nil, nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	var mu sync.Mutex
	var size int64
	done := false
	wake := make(chan struct{}, 1)
	signal := func() {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		defer func() { mu.Lock(); done = true; mu.Unlock(); signal() }()
		for {
			select {
			case <-ctx.Done():
				return
			case pcm, ok := <-audio:
				if !ok {
					return
				}
				if len(pcm) == 0 {
					continue
				}
				mu.Lock()
				offset := size
				mu.Unlock()
				if offset+int64(len(pcm)) > 1<<30 {
					failed(fmt.Errorf("audio recovery buffer reached 1 GiB; stop and start a new meeting"))
					return
				}
				if _, e := file.WriteAt(pcm, offset); e != nil {
					failed(fmt.Errorf("audio recovery buffer write failed: %w", e))
					return
				}
				mu.Lock()
				size += int64(len(pcm))
				mu.Unlock()
				signal()
			}
		}
	}()
	out := make(chan []byte)
	go func() {
		defer workers.Done()
		defer close(out)
		var offset int64
		for {
			mu.Lock()
			available, finished := size-offset, done
			mu.Unlock()
			if available == 0 {
				if finished {
					return
				}
				select {
				case <-ctx.Done():
					return
				case <-wake:
					continue
				}
			}
			chunk := int64(3200)
			if available < chunk {
				chunk = available
			}
			data := make([]byte, chunk)
			if _, e := file.ReadAt(data, offset); e != nil {
				failed(fmt.Errorf("audio recovery buffer read failed: %w", e))
				return
			}
			select {
			case <-ctx.Done():
				return
			case out <- data:
				offset += chunk
			}
		}
	}()
	cleanup := func() { cancel(); workers.Wait(); file.Close() }
	return out, cleanup, nil
}
