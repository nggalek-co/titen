package saas

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
	"titen/internal/transcription"
)

type cloudSegment struct {
	Seq     int    `json:"seq"`
	Text    string `json:"text"`
	Speaker string `json:"speaker,omitempty"`
	StartMS int64  `json:"startMs"`
	EndMS   int64  `json:"endMs"`
}

type providerUpdate struct {
	update   transcription.Update
	finished bool
	err      error
}

func (c *Client) Run(ctx context.Context, audio <-chan []byte, notify func(transcription.Update)) error {
	session, err := c.Authorize(ctx, "")
	if err != nil {
		return err
	}
	return c.Stream(ctx, session, audio, notify)
}

func (c *Client) syncSegments(ctx context.Context, sessionID string, segments []cloudSegment) error {
	var beat Heartbeat
	_, err := c.request(ctx, http.MethodPost, "/api/heartbeat", struct {
		SessionID string         `json:"sessionId"`
		Segments  []cloudSegment `json:"segments"`
	}{sessionID, segments}, &beat, true)
	if err != nil {
		return err
	}
	if !beat.OK {
		return errors.New("Titen session ended; transcript remains saved on this device")
	}
	return nil
}

func (c *Client) Stream(ctx context.Context, session Session, audio <-chan []byte, notify func(transcription.Update)) (result error) {
	if c.Token == "" {
		return errors.New("sign in to your Titen account in Settings")
	}
	if session.SessionID == "" {
		return errors.New("Titen transcription session is missing")
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := c.End(cleanupCtx, session); err != nil && result == nil {
			result = errors.New("Titen could not finalize the session; transcript remains saved on this device")
		}
	}()
	if err := session.validateProvider(); err != nil {
		return err
	}
	dialer := *websocket.DefaultDialer
	dialer.HandshakeTimeout = 20 * time.Second
	dial := dialer.DialContext
	if c.dialProvider != nil {
		dial = c.dialProvider
	}
	conn, resp, err := dial(ctx, session.WSURL, http.Header{"Authorization": []string{"Bearer " + session.TemporaryAPIKey}})
	if err != nil {
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("Soniox connection failed; start a new session for fresh temporary credentials")
	}
	defer conn.Close()
	conn.SetReadLimit(4 << 20)
	conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if err := conn.WriteJSON(map[string]any{
		"model": session.Model, "audio_format": "pcm_s16le", "sample_rate": 16000,
		"num_channels": 1, "language_hints": []string{"id", "en"},
		"enable_speaker_diarization": true, "enable_endpoint_detection": true,
	}); err != nil {
		return errors.New("Soniox stream configuration failed")
	}

	updates := make(chan providerUpdate, 64)
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		var assembler transcription.Assembler
		for {
			_, raw, readErr := conn.ReadMessage()
			event := providerUpdate{}
			if readErr != nil {
				event.err = errors.New("Soniox disconnected; start a new session for fresh temporary credentials")
			} else {
				event.update, event.finished, event.err = assembler.Update(raw)
				if event.err != nil {
					event.err = errors.New("Soniox rejected the stream or its session duration expired")
				}
				if notify != nil {
					notify(event.update)
				}
			}
			updates <- event
			if event.err != nil || event.finished {
				return
			}
		}
	}()
	pending := make([]cloudSegment, 0, 100)
	sequence := 0
	appendFinals := func(u transcription.Update) {
		for _, segment := range u.Finals {
			sequence++
			pending = append(pending, cloudSegment{sequence, segment.Text, segment.Speaker, segment.StartMS, segment.EndMS})
		}
	}
	flush := func(syncCtx context.Context) error {
		if len(pending) == 0 {
			return nil
		}
		count := min(100, len(pending))
		requestCtx, cancel := context.WithTimeout(syncCtx, 5*time.Second)
		defer cancel()
		if err := c.syncSegments(requestCtx, session.SessionID, pending[:count]); err != nil {
			return errors.New("Titen transcript sync failed; transcript remains saved on this device")
		}
		pending = pending[count:]
		return nil
	}

	defer func() {
		conn.Close()
		for {
			select {
			case event := <-updates:
				appendFinals(event.update)
			case <-readerDone:
				for len(updates) > 0 {
					appendFinals((<-updates).update)
				}
				syncCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
				defer cancel()
				for len(pending) > 0 {
					if err := flush(syncCtx); err != nil {
						result = errors.Join(result, err)
						break
					}
				}
				return
			}
		}
	}()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	keepAliveInterval := c.keepAliveInterval
	if keepAliveInterval <= 0 {
		keepAliveInterval = 10 * time.Second
	}
	keepAlive := time.NewTicker(keepAliveInterval)
	defer keepAlive.Stop()
	duration := time.NewTimer(time.Duration(session.MaxSessionDurationSeconds) * time.Second)
	defer duration.Stop()
	var finalization <-chan time.Time
	var timer *time.Timer
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-duration.C:
			return errors.New("Soniox session duration reached; start a new recording to continue")
		case event := <-updates:
			appendFinals(event.update)
			if event.err != nil {
				return event.err
			}
			if event.finished {
				return nil
			}
			if len(pending) >= 100 {
				if err := flush(ctx); err != nil {
					return err
				}
			}
		case <-ticker.C:
			if err := flush(ctx); err != nil {
				return err
			}
		case <-keepAlive.C:
			if audio != nil {
				conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
				if err := conn.WriteJSON(map[string]string{"type": "keepalive"}); err != nil {
					return errors.New("Soniox keepalive failed; start a new session to reconnect")
				}
			}
		case <-finalization:
			return errors.New("Soniox transcription finalization timed out")
		case pcm, ok := <-audio:
			conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if !ok {
				if err := conn.WriteMessage(websocket.TextMessage, nil); err != nil {
					return errors.New("Soniox could not finish the transcription stream")
				}
				audio = nil
				timer = time.NewTimer(15 * time.Second)
				finalization = timer.C
				continue
			}
			if len(pcm) == 0 {
				continue
			}
			if len(pcm)%2 != 0 {
				return errors.New("Titen requires 16-bit mono PCM audio")
			}
			if err := conn.WriteMessage(websocket.BinaryMessage, pcm); err != nil {
				return errors.New("Soniox audio upload failed; start a new session to reconnect")
			}
		}
	}
}
