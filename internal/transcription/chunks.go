package transcription

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

// ChunkRecorder writes the opt-in full-meeting recording: bounded WAV
// files rotated every ChunkMaxMS with a short overlap so speech is not
// cut between files. Each finished file gets a checksum and emits a
// ChunkMeta for the caller to persist. The temporary reconnect spool is
// not the archive; this is.
const (
	ChunkMaxMS      = 60 * 60 * 1000 // 60 minutes, under Soniox's 300-minute cap
	chunkOverlapMS  = 2_000
	pcmBytesPerMS   = 32 // 16 kHz mono s16le
	chunkWavHeader  = 44
)

type ChunkMeta struct {
	ID         string
	MeetingID  string
	Index      int
	StartMS    int64
	DurationMS int64
	Checksum   string
	Path       string
	SizeBytes  int64
}

type Recorder struct {
	dir       string
	meetingID string
	limit     int64
	onChunk   func(ChunkMeta)
	failed    func(error)
	newID     func() string

	file    *os.File
	name    string
	index   int
	startMS int64
	written int64 // PCM bytes in the open file
	tail    []byte // trailing audio replayed at the next chunk's start
	total   int64  // bytes on disk for this meeting
	done    bool
}

// NewRecorder starts a chunk writer in dir for one meeting. onChunk fires
// for every sealed file; failed reports a write or capacity problem once.
func NewRecorder(dir, meetingID string, limitBytes int64, onChunk func(ChunkMeta), failed func(error), newID func() string) (*Recorder, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	r := &Recorder{dir: dir, meetingID: meetingID, limit: limitBytes, onChunk: onChunk, failed: failed, newID: newID}
	if err := r.open(0); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *Recorder) writeHeader() {
	var header [chunkWavHeader]byte
	copy(header[0:], "RIFF")
	binary.LittleEndian.PutUint32(header[4:], 36+uint32(r.written))
	copy(header[8:], "WAVE")
	copy(header[12:], "fmt ")
	binary.LittleEndian.PutUint32(header[16:], 16)
	binary.LittleEndian.PutUint16(header[20:], 1) // PCM
	binary.LittleEndian.PutUint16(header[22:], 1) // mono
	binary.LittleEndian.PutUint32(header[24:], 16000)
	binary.LittleEndian.PutUint32(header[28:], 16000*2)
	binary.LittleEndian.PutUint16(header[32:], 2)
	binary.LittleEndian.PutUint16(header[34:], 16)
	copy(header[36:], "data")
	binary.LittleEndian.PutUint32(header[40:], uint32(r.written))
	_, _ = r.file.WriteAt(header[:], 0)
}

func (r *Recorder) open(startMS int64) error {
	r.index++
	name := fmt.Sprintf("%s-%03d.wav", r.meetingID, r.index)
	path := filepath.Join(r.dir, name)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	r.file, r.name, r.startMS, r.written = file, path, startMS, 0
	if _, err = r.file.Write(make([]byte, chunkWavHeader)); err != nil {
		return err
	}
	// Overlap: the previous chunk's tail opens this one so no speech is
	// lost at the boundary. It is duplicated audio, deduplicated later by
	// the merge on absolute offsets.
	if startMS > 0 && len(r.tail) > 0 {
		if _, err = r.file.Write(r.tail); err != nil {
			return err
		}
		r.written = int64(len(r.tail))
	}
	return nil
}

func (r *Recorder) fail(err error) {
	if r.failed != nil {
		r.failed(err)
	}
	r.done = true
}

// Write appends one PCM buffer. The audio path calls it per pipeline
// chunk; files stay small and writes are sequential.
func (r *Recorder) Write(pcm []byte) {
	if r.done || len(pcm) == 0 {
		return
	}
	maxBytes := int64(ChunkMaxMS * pcmBytesPerMS)
	if r.written+int64(len(pcm)) > maxBytes {
		// Seal at the boundary, then continue into the next file.
		cut := maxBytes - r.written
		r.append(pcm[:cut])
		r.rotate()
		r.Write(pcm[cut:])
		return
	}
	r.append(pcm)
}

func (r *Recorder) append(pcm []byte) {
	if _, err := r.file.Write(pcm); err != nil {
		r.fail(fmt.Errorf("audio chunk write failed: %w", err))
		return
	}
	r.written += int64(len(pcm))
}

func (r *Recorder) rotate() {
	overlap := int64(chunkOverlapMS * pcmBytesPerMS)
	if r.written > overlap {
		r.tail = make([]byte, overlap)
		_, _ = r.file.ReadAt(r.tail, chunkWavHeader+r.written-overlap)
	} else {
		r.tail = nil
	}
	r.seal()
	next := r.startMS + int64(ChunkMaxMS)
	if err := r.open(next); err != nil {
		r.fail(fmt.Errorf("audio chunk rotation failed: %w", err))
	}
}

func (r *Recorder) seal() {
	r.writeHeader()
	_ = r.file.Sync()
	_ = r.file.Close()
	data, err := os.ReadFile(r.name)
	if err != nil {
		r.fail(fmt.Errorf("audio chunk checksum failed: %w", err))
		return
	}
	sum := sha256.Sum256(data)
	r.total += int64(len(data))
	if r.limit > 0 && r.total > r.limit {
		r.fail(fmt.Errorf("audio recording reached its %d MiB disk limit; stop and start a new meeting", r.limit>>20))
		return
	}
	r.onChunk(ChunkMeta{
		ID:         r.newID(),
		MeetingID:  r.meetingID,
		Index:      r.index - 1,
		StartMS:    r.startMS,
		DurationMS: r.written / pcmBytesPerMS,
		Checksum:   hex.EncodeToString(sum[:]),
		Path:       r.name,
		SizeBytes:  int64(len(data)),
	})
}

// Close seals the open chunk. The last file may be short — that is fine.
func (r *Recorder) Close() {
	if r.done && r.file == nil {
		return
	}
	r.done = true
	if r.file != nil {
		r.seal()
		r.file = nil
	}
}

// WavHeader renders a 44-byte header for tests and readers.
func WavHeader(pcmBytes uint32) [chunkWavHeader]byte {
	var header [chunkWavHeader]byte
	copy(header[0:], "RIFF")
	binary.LittleEndian.PutUint32(header[4:], 36+pcmBytes)
	copy(header[8:], "WAVE")
	copy(header[12:], "fmt ")
	binary.LittleEndian.PutUint32(header[16:], 16)
	binary.LittleEndian.PutUint16(header[20:], 1)
	binary.LittleEndian.PutUint16(header[22:], 1)
	binary.LittleEndian.PutUint32(header[24:], 16000)
	binary.LittleEndian.PutUint32(header[28:], 32000)
	binary.LittleEndian.PutUint16(header[32:], 2)
	binary.LittleEndian.PutUint16(header[34:], 16)
	copy(header[36:], "data")
	binary.LittleEndian.PutUint32(header[40:], pcmBytes)
	return header
}
