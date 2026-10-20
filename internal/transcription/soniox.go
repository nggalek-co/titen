package transcription

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gorilla/websocket"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"titen/internal/transcript"
)

type Client struct {
	APIKey, URL, Model string
	SampleRate         int
	Languages          []string
	Dialer             *websocket.Dialer
}
type Update struct {
	Partial string
	Finals  []transcript.Segment
	Err     error
}
type token struct {
	Text    string `json:"text"`
	Start   int64  `json:"start_ms"`
	End     int64  `json:"end_ms"`
	Speaker string `json:"speaker"`
	Final   bool   `json:"is_final"`
}
type response struct {
	Tokens    []token `json:"tokens"`
	Finished  bool    `json:"finished"`
	ErrorCode int     `json:"error_code"`
	ErrorType string  `json:"error_type"`
}
type providerError struct {
	code int
	kind string
}

func (e providerError) Error() string { return fmt.Sprintf("Soniox %s (%d)", e.kind, e.code) }

type Assembler struct {
	Offset    int64
	seen      map[string]int64
	watermark int64
}

func (a *Assembler) Update(raw []byte) (Update, bool, error) {
	var r response
	if err := json.Unmarshal(raw, &r); err != nil {
		return Update{}, false, err
	}
	if r.ErrorCode != 0 {
		return Update{}, false, providerError{r.ErrorCode, r.ErrorType}
	}
	u := Update{}
	if a.seen == nil {
		a.seen = map[string]int64{}
	}
	for _, t := range r.Tokens {
		if strings.HasPrefix(t.Text, "<") {
			continue
		}
		if !t.Final {
			u.Partial += t.Text
			continue
		}
		s := transcript.Segment{Speaker: t.Speaker, Text: t.Text, StartMS: a.Offset + t.Start, EndMS: a.Offset + t.End}
		s.ID = s.Key()
		if _, exists := a.seen[s.ID]; !exists && s.EndMS >= a.watermark-60000 {
			a.seen[s.ID] = s.EndMS
			if s.EndMS > a.watermark {
				a.watermark = s.EndMS
			}
			if n := len(u.Finals); n > 0 && u.Finals[n-1].Speaker == s.Speaker {
				u.Finals[n-1].Text += s.Text
				u.Finals[n-1].EndMS = s.EndMS
				u.Finals[n-1].ID = u.Finals[n-1].Key()
			} else {
				u.Finals = append(u.Finals, s)
			}
		}
	}
	for key, end := range a.seen {
		if end < a.watermark-60000 {
			delete(a.seen, key)
		}
	}
	return u, r.Finished, nil
}
func (c *Client) Run(ctx context.Context, audio <-chan []byte, notify func(Update)) error {
	if c.APIKey == "" {
		return fmt.Errorf("Soniox API key required")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	spooled, cleanup, err := spool(ctx, audio, func(err error) { notify(Update{Err: err}); cancel() })
	if err != nil {
		return err
	}
	defer cleanup()
	return c.stream(ctx, spooled, notify)
}
func (c *Client) stream(ctx context.Context, audio <-chan []byte, notify func(Update)) error {
	rate := c.SampleRate
	if rate == 0 {
		rate = 16000
	}
	endpoint := c.URL
	if endpoint == "" {
		endpoint = "wss://stt-rt.soniox.com/transcribe-websocket"
	}
	model := c.Model
	if model == "" {
		model = "stt-rt-v5"
	}
	var samples int64
	dialer := c.Dialer
	if dialer == nil {
		dialer = websocket.DefaultDialer
	}
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		conn, handshake, err := dialer.DialContext(ctx, endpoint, http.Header{"Authorization": []string{"Bearer " + c.APIKey}})
		if err != nil {
			code := 0
			if handshake != nil {
				code = handshake.StatusCode
				if handshake.Body != nil {
					handshake.Body.Close()
				}
			}
			if code == 401 || code == 403 {
				return fmt.Errorf("Soniox rejected the API key or account access (HTTP %d); update credentials in Settings", code)
			}
			slog.Warn("transcription connection retry", "http_status", code)
			notify(Update{Err: fmt.Errorf("Soniox connection unavailable")})
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Second):
			}
			continue
		}
		conn.SetReadLimit(4 << 20)
		conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		err = conn.WriteJSON(map[string]any{"model": model, "audio_format": "pcm_s16le", "sample_rate": rate, "num_channels": 1, "language_hints": c.Languages, "enable_speaker_diarization": true, "enable_endpoint_detection": true})
		if err != nil {
			conn.Close()
			continue
		}
		a := Assembler{Offset: samples * 1000 / int64(rate)}
		read := make(chan error, 1)
		readerDone := make(chan struct{})
		go func() {
			defer close(readerDone)
			for {
				_, data, e := conn.ReadMessage()
				if e != nil {
					read <- e
					return
				}
				u, done, e := a.Update(data)
				if e != nil {
					read <- e
					return
				}
				notify(u)
				if done {
					read <- nil
					return
				}
			}
		}()
		keep := time.NewTicker(15 * time.Second)
		rotate := time.NewTimer(290 * time.Minute)
		closed := false
		var streamErr error
	loop:
		for {
			select {
			case <-ctx.Done():
				streamErr = ctx.Err()
				break loop
			case streamErr = <-read:
				break loop
			case <-rotate.C:
				conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
				conn.WriteMessage(websocket.TextMessage, nil)
				select {
				case streamErr = <-read:
				case <-time.After(15 * time.Second):
				}
				break loop
			case <-keep.C:
				conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
				if streamErr = conn.WriteJSON(map[string]string{"type": "keepalive"}); streamErr != nil {
					break loop
				}
			case pcm, ok := <-audio:
				if !ok {
					closed = true
					conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
					conn.WriteMessage(websocket.TextMessage, nil)
					select {
					case streamErr = <-read:
					case <-time.After(15 * time.Second):
						streamErr = fmt.Errorf("Soniox finalization timed out")
					}
					break loop
				}
				if len(pcm) == 0 {
					continue
				}
				conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
				streamErr = conn.WriteMessage(websocket.BinaryMessage, pcm)
				samples += int64(len(pcm) / 2)
				if streamErr != nil {
					break loop
				}
			}
		}
		keep.Stop()
		rotate.Stop()
		conn.Close()
		<-readerDone
		var permanent providerError
		if errors.As(streamErr, &permanent) && permanent.code < 500 && permanent.code != 429 {
			return streamErr
		}
		if closed {
			return streamErr
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		notify(Update{Err: fmt.Errorf("Soniox disconnected. Reconnecting; unconfirmed audio may be lost")})
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}
