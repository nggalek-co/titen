package app

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"

	"titen/internal/saas"
	"titen/internal/storage"
)

// retainOrUpload runs when a meeting ends. Retained audio earns its keep
// only when realtime coverage has a gap: a clean meeting deletes its local
// chunks, a meeting that lost transcription uploads for delayed recovery.
// Nothing is uploaded unless the user opted in and recovery is needed.
func (s *Service) retainOrUpload(ctx context.Context, meetingID string, needsRecovery bool) {
	chunks, err := s.Store.AudioChunks(ctx, meetingID)
	if err != nil || len(chunks) == 0 {
		return
	}
	if !needsRecovery {
		for _, chunk := range chunks {
			_ = os.Remove(chunk.Path)
			_ = s.Store.SetAudioChunkState(ctx, chunk.ID, "deleted")
		}
		_ = os.RemoveAll(filepath.Join(s.RecordingsDir, meetingID))
		return
	}
	go s.uploadMeetingAudio(context.WithoutCancel(ctx), meetingID)
}

// uploadMeetingAudio registers each finished chunk, PUTs it to the
// presigned object URL, then drops the local copy — the remote object is
// the surviving one. Failed uploads keep the file and retry on the next
// app start via ResumeAudioUploads.
func (s *Service) uploadMeetingAudio(ctx context.Context, meetingID string) {
	client := s.client
	if client == nil {
		return
	}
	chunks, err := s.Store.AudioChunks(ctx, meetingID)
	if err != nil || len(chunks) == 0 {
		return
	}
	meeting, err := s.Store.Meeting(ctx, meetingID)
	if err != nil {
		return
	}
	cloudID := meeting.CloudMeetingID
	if cloudID == "" {
		cloudID, err = client.CreateCloudMeeting(ctx, meeting.Title)
		if err != nil {
			slog.Warn("audio recovery upload could not create meeting", "meeting_id", meetingID, "error", err)
			return
		}
		if err = s.Store.SetCloudMeeting(ctx, meetingID, client.BaseURL, cloudID); err != nil {
			return
		}
	}
	for _, chunk := range chunks {
		if err := ctx.Err(); err != nil {
			return
		}
		if chunk.State != "local_ready" && chunk.State != "failed" {
			continue
		}
		if err := s.uploadChunk(ctx, client, cloudID, chunk); err != nil {
			slog.Warn("audio chunk upload failed", "meeting_id", meetingID, "chunk", chunk.Index, "error", err)
			_ = s.Store.SetAudioChunkState(ctx, chunk.ID, "failed")
		}
	}
}

func (s *Service) uploadChunk(ctx context.Context, client *saas.Client, cloudMeetingID string, chunk storage.AudioChunk) error {
	registration, err := client.RegisterChunk(ctx, struct {
		MeetingID  string `json:"meetingId"`
		ChunkIndex int    `json:"chunkIndex"`
		StartMS    int64  `json:"startMs"`
		DurationMS int64  `json:"durationMs"`
		Format     string `json:"format"`
		Checksum   string `json:"checksum"`
		SizeBytes  int64  `json:"sizeBytes"`
		Consent    bool   `json:"consent"`
	}{cloudMeetingID, chunk.Index, chunk.StartMS, chunk.DurationMS, "wav", chunk.Checksum, chunk.SizeBytes, true})
	if err != nil {
		return err
	}
	if registration.Stored {
		return s.completeUpload(ctx, chunk)
	}
	data, err := os.ReadFile(chunk.Path)
	if err != nil {
		return err
	}
	if err := client.UploadAudio(ctx, registration.UploadURL, data); err != nil {
		return err
	}
	if err := client.MarkChunkUploaded(ctx, registration.ChunkID); err != nil {
		return err
	}
	return s.completeUpload(ctx, chunk)
}

// completeUpload marks the chunk stored and drops the local file: after a
// confirmed remote copy, keeping the duplicate would only burn the disk
// budget. A failed upload earlier never reaches here — that copy stays.
func (s *Service) completeUpload(ctx context.Context, chunk storage.AudioChunk) error {
	if err := s.Store.SetAudioChunkState(ctx, chunk.ID, "stored"); err != nil {
		return err
	}
	_ = os.Remove(chunk.Path)
	return nil
}

// ResumeAudioUploads settles crash-interrupted chunk state and restarts
// pending uploads, grouped per meeting so each runs once.
func (s *Service) ResumeAudioUploads(ctx context.Context, client *saas.Client) {
	if client == nil || client.Token == "" {
		return
	}
	s.client = client
	_ = s.Store.RecoverAudioChunks(ctx)
	pending, err := s.Store.PendingAudioUploads(ctx)
	if err != nil || len(pending) == 0 {
		return
	}
	seen := map[string]bool{}
	for _, chunk := range pending {
		if seen[chunk.MeetingID] {
			continue
		}
		seen[chunk.MeetingID] = true
		meetingID := chunk.MeetingID
		go func() {
			if err := ctx.Err(); err == nil {
				s.uploadMeetingAudio(ctx, meetingID)
			}
		}()
	}
}


