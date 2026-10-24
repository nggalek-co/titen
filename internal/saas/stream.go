package saas

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
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

var errTranscriptSyncDeferred = errors.New("Titen transcript upload will retry")
var errProviderInterrupted = errors.New("Soniox connection interrupted")
var errProviderCapacity = errors.New("Soniox capacity limit reached")

type streamCursor struct {
	sequence       int
	pending        []cloudSegment
	retryAudio     []byte
	audioBytesSent int64
}

const pcmBytesPerSecond = 16000 * 2

func (cursor *streamCursor) offsetMs() int64 {
	return cursor.audioBytesSent * 1000 / pcmBytesPerSecond
}

func providerInterrupted(message string) error {
	return fmt.Errorf("%w: %s", errProviderInterrupted, message)
}

func transientBackendError(err error) bool {
	if errors.Is(err, ErrUnavailable) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var apiErr *APIError
	return errors.As(err, &apiErr) && (apiErr.Status == 429 || apiErr.Status >= 500)
}

func transientRenewalError(err error) bool {
	if transientBackendError(err) {
		return true
	}
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Status == 409
}

// retryDelay honors the server's Retry-After hint before the default wait,
// so a Titen capacity response paces the fleet instead of fanning retries.
func retryDelay(err error, fallback time.Duration) time.Duration {
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.RetryAfter > fallback {
		return apiErr.RetryAfter
	}
	return fallback
}

// reportProviderError ships the provider's stable error_type and
// request_id upstream without blocking or failing the stream on telemetry.
func (c *Client) reportProviderError(sessionID string, perr *transcription.Error) {
	go func() {
		reportCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = c.ReportProviderError(reportCtx, sessionID, perr.Kind, perr.RequestID)
	}()
}

// classifyStreamError reports the provider error and decides how the
// meeting continues: typed Soniox error frames get a retry posture, and
// anything else keeps the existing reconnect behavior.
func (c *Client) classifyStreamError(sessionID string, err error) error {
	var perr *transcription.Error
	if !errors.As(err, &perr) {
		return err
	}
	c.reportProviderError(sessionID, perr)
	switch transcription.ClassifyProviderError(perr) {
	case transcription.ErrorFatal:
		return perr
	case transcription.ErrorCapacity:
		return fmt.Errorf("%w: %s", errProviderCapacity, perr.Kind)
	default:
		// Transient failures and grant/stream duration caps all continue the
		// meeting on the next grant; the renewal path fetches a fresh key.
		return providerInterrupted(perr.Kind)
	}
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
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := c.End(cleanupCtx, session); err != nil && result == nil {
			result = errors.New("Titen could not finalize the session; transcript remains saved on this device")
		}
	}()
	cursor := &streamCursor{}
	_, result = c.streamGrant(ctx, session, audio, notify, cursor, 0, nil, nil)
	return result
}

// StreamMeeting rotates short provider grants without ending the server meeting.
func (c *Client) StreamMeeting(ctx context.Context, session Session, audio <-chan []byte, notify func(transcription.Update), changed func(string)) (result error) {
	current := session
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := c.End(cleanupCtx, current); err != nil && result == nil {
			result = errors.New("Titen could not finalize the session; transcript remains saved on this device")
		}
	}()
	streamCtx, cancelStream := context.WithCancel(ctx)
	defer cancelStream()
	spoolFailure := make(chan error, 1)
	spooled, cleanup, err := transcription.Spool(streamCtx, audio, func(err error) {
		select {
		case spoolFailure <- err:
		default:
		}
		cancelStream()
	})
	if err != nil {
		return err
	}
	defer cleanup()
	cursor := &streamCursor{}
	reconnectDelay := 250 * time.Millisecond
	waitReconnect := func() error {
		timer := time.NewTimer(reconnectDelay)
		reconnectDelay = min(reconnectDelay*2, 5*time.Second)
		defer timer.Stop()
		select {
		case err := <-spoolFailure:
			return err
		case <-streamCtx.Done():
			select {
			case err := <-spoolFailure:
				return err
			default:
				return streamCtx.Err()
			}
		case <-timer.C:
			return nil
		}
	}
	for {
		if changed != nil {
			changed(current.SessionID)
		}
		rotate := make(chan Session)
		renewalFailure := make(chan error, 1)
		grantCtx, cancel := context.WithCancel(streamCtx)
		go func(s Session) {
			grantEnd := s.ExpiresAt.Add(time.Duration(s.MaxSessionDurationSeconds) * time.Second)
			// Renew 90–120s early: jitter staggers the fleet's renewal rate
			// instead of synchronizing every meeting on the same lead.
			wait := time.Until(grantEnd.Add(-90*time.Second - time.Duration(rand.Int64N(int64(30*time.Second)))))
			if wait > 0 {
				timer := time.NewTimer(wait)
				defer timer.Stop()
				select {
				case <-grantCtx.Done():
					return
				case <-timer.C:
				}
			}
			for grantCtx.Err() == nil {
				next, err := c.AuthorizeRenewal(grantCtx, s.MeetingID)
				if err == nil {
					select {
					case rotate <- next:
					case <-grantCtx.Done():
						cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
						_ = c.End(cleanupCtx, next)
						cleanupCancel()
					}
					return
				}
				if !transientRenewalError(err) {
					select {
					case renewalFailure <- err:
					case <-grantCtx.Done():
					}
					return
				}
				timer := time.NewTimer(retryDelay(err, 5*time.Second))
				select {
				case <-grantCtx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
			}
		}(current)
		next, err := c.streamGrant(streamCtx, current, spooled, notify, cursor, cursor.offsetMs(), rotate, renewalFailure)
		cancel()
		if next != nil {
			current = *next
		}
		if err != nil {
			select {
			case spoolErr := <-spoolFailure:
				return spoolErr
			default:
			}
			if streamCtx.Err() != nil {
				return streamCtx.Err()
			}
			if errors.Is(err, errProviderCapacity) {
				// The provider's own quota rejected the stream. Retrying in a
				// tight loop only deepens the rejection, so reconnects are
				// spaced tens of seconds apart and still honor Retry-After.
				if notify != nil {
					notify(transcription.Update{ConnectionStatus: transcription.ConnectionReconnecting})
				}
				capacityDelay := 30 * time.Second
				for {
					timer := time.NewTimer(capacityDelay + time.Duration(rand.Int64N(int64(capacityDelay/4))))
					capacityDelay = min(capacityDelay*2, 5*time.Minute)
					select {
					case spoolErr := <-spoolFailure:
						timer.Stop()
						return spoolErr
					case <-streamCtx.Done():
						timer.Stop()
						return streamCtx.Err()
					case <-timer.C:
					}
					replacement, authErr := c.AuthorizeRenewal(streamCtx, current.MeetingID)
					if authErr == nil {
						current = replacement
						break
					}
					if !transientRenewalError(authErr) {
						return authErr
					}
					if wait := retryDelay(authErr, 0); wait > capacityDelay {
						capacityDelay = wait
					}
				}
				continue
			}
			if !errors.Is(err, errProviderInterrupted) {
				return err
			}
			if notify != nil {
				notify(transcription.Update{ConnectionStatus: transcription.ConnectionReconnecting})
			}
			for {
				if err := waitReconnect(); err != nil {
					return err
				}
				if next != nil {
					break
				}
				replacement, err := c.AuthorizeRenewal(streamCtx, current.MeetingID)
				if err == nil {
					current = replacement
					break
				}
				if !transientRenewalError(err) {
					return err
				}
				if wait := retryDelay(err, 0); wait > reconnectDelay {
					reconnectDelay = wait
				}
			}
			continue
		}
		reconnectDelay = 250 * time.Millisecond
		if next == nil {
			return nil
		}
	}
}

func (c *Client) streamGrant(ctx context.Context, session Session, audio <-chan []byte, notify func(transcription.Update), cursor *streamCursor, offsetMs int64, rotate <-chan Session, renewalFailure <-chan error) (next *Session, result error) {
	renewable := rotate != nil
	if c.Token == "" {
		return nil, errors.New("sign in to your Titen account in Settings")
	}
	if session.SessionID == "" {
		return nil, errors.New("Titen transcription session is missing")
	}
	if err := session.validateProvider(); err != nil {
		return nil, err
	}
	dialer := *websocket.DefaultDialer
	dialer.HandshakeTimeout = 20 * time.Second
	dial := dialer.DialContext
	if c.dialProvider != nil {
		dial = c.dialProvider
	}
	conn, resp, err := dial(ctx, session.WSURL, http.Header{"Authorization": []string{"Bearer " + session.TemporaryAPIKey}})
	if err != nil {
		code := 0
		if resp != nil {
			code = resp.StatusCode
			resp.Body.Close()
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if code == http.StatusTooManyRequests {
			return nil, fmt.Errorf("%w: handshake rejected (HTTP 429)", errProviderCapacity)
		}
		return nil, providerInterrupted("Soniox connection failed")
	}
	defer conn.Close()
	conn.SetReadLimit(4 << 20)
	conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if err := conn.WriteJSON(map[string]any{
		"model": session.Model, "audio_format": "pcm_s16le", "sample_rate": 16000,
		"num_channels": 1, "language_hints": []string{"id", "en"},
		"enable_speaker_diarization": true, "enable_endpoint_detection": true,
	}); err != nil {
		return nil, providerInterrupted("Soniox stream configuration failed")
	}
	if notify != nil {
		notify(transcription.Update{ConnectionStatus: transcription.ConnectionConnected})
	}
	if len(cursor.retryAudio) > 0 {
		conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if err := conn.WriteMessage(websocket.BinaryMessage, cursor.retryAudio); err != nil {
			return nil, providerInterrupted("Soniox audio upload failed")
		}
		cursor.audioBytesSent += int64(len(cursor.retryAudio))
		cursor.retryAudio = nil
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
				event.err = providerInterrupted("Soniox disconnected")
			} else {
				event.update, event.finished, event.err = assembler.Update(raw)
				for i := range event.update.Finals {
					event.update.Finals[i].StartMS += offsetMs
					event.update.Finals[i].EndMS += offsetMs
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
	appendFinals := func(u transcription.Update) {
		for _, segment := range u.Finals {
			cursor.sequence++
			cursor.pending = append(cursor.pending, cloudSegment{cursor.sequence, segment.Text, segment.Speaker, segment.StartMS, segment.EndMS})
		}
	}
	flush := func(syncCtx context.Context) error {
		if len(cursor.pending) == 0 {
			return nil
		}
		count := min(100, len(cursor.pending))
		requestCtx, cancel := context.WithTimeout(syncCtx, 5*time.Second)
		defer cancel()
		if err := c.syncSegments(requestCtx, session.SessionID, cursor.pending[:count]); err != nil {
			if renewable && transientBackendError(err) {
				return errTranscriptSyncDeferred
			}
			return errors.New("Titen transcript sync failed; transcript remains saved on this device")
		}
		cursor.pending = cursor.pending[count:]
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
				if renewable && (errors.Is(result, errProviderInterrupted) || (next != nil && result == nil)) {
					return
				}
				syncCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
				defer cancel()
				for len(cursor.pending) > 0 {
					if err := flush(syncCtx); err != nil {
						if errors.Is(err, errTranscriptSyncDeferred) && syncCtx.Err() == nil {
							timer := time.NewTimer(time.Second)
							select {
							case <-timer.C:
							case <-syncCtx.Done():
								timer.Stop()
							}
							continue
						}
						if errors.Is(err, errTranscriptSyncDeferred) {
							err = errors.New("Titen transcript sync timed out; transcript remains saved on this device")
						}
						result = errors.Join(result, err)
						break
					}
				}
				return
			}
		}
	}()
	syncInterval := c.syncInterval
	if syncInterval <= 0 {
		syncInterval = 5 * time.Second
	}
	ticker := time.NewTicker(syncInterval)
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
			return next, ctx.Err()
		case <-duration.C:
			if renewable {
				return next, providerInterrupted("Soniox session duration reached before renewal completed")
			}
			return next, errors.New("Soniox session duration reached before renewal completed")
		case err := <-renewalFailure:
			return next, err
		case replacement := <-rotate:
			next = &replacement
			conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := conn.WriteMessage(websocket.TextMessage, nil); err != nil {
				return next, errors.New("Soniox could not finish the transcription stream")
			}
			audio = nil
			rotate = nil
			timer = time.NewTimer(15 * time.Second)
			finalization = timer.C
		case event := <-updates:
			appendFinals(event.update)
			if len(cursor.pending) > 1000 {
				return next, errors.New("Titen transcript upload queue is full; transcript remains saved on this device")
			}
			if event.err != nil {
				classified := c.classifyStreamError(session.SessionID, event.err)
				if audio == nil && errors.Is(classified, errProviderInterrupted) {
					return next, errors.New("Soniox disconnected before finalizing; unfinalized speech may be missing")
				}
				return next, classified
			}
			if event.finished {
				if renewable && audio != nil && next == nil {
					return next, providerInterrupted("Soniox stream ended before transcription renewal completed")
				}
				return next, nil
			}
			if len(cursor.pending) >= 100 {
				if err := flush(ctx); err != nil && !errors.Is(err, errTranscriptSyncDeferred) {
					return next, err
				}
			}
		case <-ticker.C:
			if err := flush(ctx); err != nil && !errors.Is(err, errTranscriptSyncDeferred) {
				return next, err
			}
		case <-keepAlive.C:
			if audio != nil {
				conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
				if err := conn.WriteJSON(map[string]string{"type": "keepalive"}); err != nil {
					return next, providerInterrupted("Soniox keepalive failed")
				}
			}
		case <-finalization:
			return next, errors.New("Soniox transcription finalization timed out")
		case pcm, ok := <-audio:
			conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if !ok {
				if err := conn.WriteMessage(websocket.TextMessage, nil); err != nil {
					return next, errors.New("Soniox could not finish the transcription stream")
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
				return next, errors.New("Titen requires 16-bit mono PCM audio")
			}
			if err := conn.WriteMessage(websocket.BinaryMessage, pcm); err != nil {
				cursor.retryAudio = pcm
				return next, providerInterrupted("Soniox audio upload failed")
			}
			cursor.audioBytesSent += int64(len(pcm))
		}
	}
}
