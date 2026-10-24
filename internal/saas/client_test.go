package saas

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/gorilla/websocket"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"titen/internal/transcription"
)

func TestEndpoint(t *testing.T) {
	for _, input := range []string{"", "http://localhost:3000/", "http://127.0.0.1:8787", "http://[::1]:3000", "https://titen.example"} {
		if _, err := NormalizeEndpoint(input); err != nil {
			t.Errorf("%q: %v", input, err)
		}
	}
	for _, input := range []string{"http://evil.example", "https://user:secret@example.org", "https://example.org/path", "https://example.org?token=secret", "file:///tmp/test", "http://localhost.evil:3000"} {
		if _, err := NormalizeEndpoint(input); err == nil {
			t.Errorf("accepted %q", input)
		}
	}
	if TokenKey("") != TokenKey(DefaultEndpoint+"/") {
		t.Fatal("equivalent endpoints have different token keys")
	}
	if TokenKey("http://localhost:3001") == TokenKey(DefaultEndpoint) {
		t.Fatal("token is not scoped to endpoint")
	}
}

func TestDevicePendingThenApproval(t *testing.T) {
	var calls atomic.Int32
	var origin string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Error("not POST")
		}
		switch r.URL.Path {
		case "/api/device/codes":
			json.NewEncoder(w).Encode(map[string]any{"code": "ABCDEFGH", "verifyUrl": origin + "/device?code=ABCDEFGH", "expiresInSeconds": 600})
		case "/api/device/token":
			if calls.Add(1) == 1 {
				w.WriteHeader(202)
				w.Write([]byte(`{"pending":true}`))
			} else {
				w.Write([]byte(`{"token":"ttn_dev_test"}`))
			}
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	}))
	defer server.Close()
	origin = server.URL
	client := Client{BaseURL: server.URL}
	code, err := client.BeginDevice(context.Background(), "Test desktop")
	if err != nil {
		t.Fatal(err)
	}
	token, err := client.WaitDevice(context.Background(), code)
	if err != nil || token != "ttn_dev_test" || calls.Load() != 2 {
		t.Fatalf("token=%q calls=%d err=%v", token, calls.Load(), err)
	}
}

func TestDeviceCancel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(202); w.Write([]byte(`{"pending":true}`)) }))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	_, err := (&Client{BaseURL: server.URL}).WaitDevice(ctx, DeviceCode{Code: "CODE", ExpiresInSeconds: 600})
	if err != context.DeadlineExceeded {
		t.Fatalf("got %v", err)
	}
}

func TestNoRedirectOrSecretLeak(t *testing.T) {
	var reached atomic.Bool
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached.Store(true) }))
	defer other.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"?secret=private", http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	_, err := (&Client{BaseURL: server.URL, Token: "private"}).Authorize(context.Background(), "")
	if err == nil || strings.Contains(err.Error(), "private") || reached.Load() {
		t.Fatalf("redirect or secret leak, reached=%v err=%v", reached.Load(), err)
	}
}

func TestAuthorizeRejectsForeignStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Error("missing device authentication")
		}
		w.Write([]byte(`{"sessionId":"s","meetingId":"m","reservationId":"r","wsUrl":"wss://other.example/api/stream"}`))
	}))
	defer server.Close()
	_, err := (&Client{BaseURL: server.URL, Token: "secret"}).Authorize(context.Background(), "test")
	if err == nil {
		t.Fatal("accepted foreign stream")
	}
}

func directSession() Session {
	return Session{SessionID: "s", MeetingID: "m", ReservationID: "r", WSURL: "wss://stt-rt.soniox.com/transcribe-websocket", TemporaryAPIKey: "temporary-secret", ExpiresAt: time.Now().Add(time.Minute), MaxSessionDurationSeconds: 600, Model: "stt-rt-v5"}
}

type testTransport func(*http.Request) (*http.Response, error)

func (f testTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type pipeListener struct{ conn net.Conn }

func (p *pipeListener) Accept() (net.Conn, error) {
	if p.conn == nil {
		return nil, io.EOF
	}
	c := p.conn
	p.conn = nil
	return c, nil
}
func (p *pipeListener) Close() error   { return nil }
func (p *pipeListener) Addr() net.Addr { return pipeAddr{} }

type pipeAddr struct{}

func (pipeAddr) Network() string { return "pipe" }
func (pipeAddr) String() string  { return "pipe" }

func directTestClient(t *testing.T, provider http.HandlerFunc, backend http.HandlerFunc) *Client {
	t.Helper()
	return &Client{BaseURL: "http://localhost:3000", Token: "device-secret", HTTP: &http.Client{Transport: testTransport(func(r *http.Request) (*http.Response, error) {
		recorder := httptest.NewRecorder()
		backend(recorder, r)
		return recorder.Result(), nil
	})}, dialProvider: func(ctx context.Context, endpoint string, headers http.Header) (*websocket.Conn, *http.Response, error) {
		if endpoint != directSession().WSURL {
			t.Errorf("provider endpoint %q", endpoint)
		}
		serverConn, clientConn := net.Pipe()
		server := &http.Server{Handler: provider}
		go server.Serve(&pipeListener{conn: serverConn})
		t.Cleanup(func() { server.Close(); serverConn.Close(); clientConn.Close() })
		dialer := websocket.Dialer{NetDialContext: func(context.Context, string, string) (net.Conn, error) { return clientConn, nil }, HandshakeTimeout: time.Second}
		return dialer.DialContext(ctx, "ws://provider", headers)
	}}
}

func TestStreamAudioThenEnd(t *testing.T) {
	var ended, synced atomic.Bool
	var saved atomic.Bool
	client := directTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer temporary-secret" {
			t.Error("wrong provider authentication")
		}
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		var config map[string]any
		if err := conn.ReadJSON(&config); err != nil {
			t.Error(err)
			return
		}
		if config["model"] != "stt-rt-v5" || config["audio_format"] != "pcm_s16le" || config["sample_rate"] != float64(16000) || config["num_channels"] != float64(1) {
			t.Errorf("config=%v", config)
		}
		kind, data, err := conn.ReadMessage()
		if err != nil || kind != websocket.BinaryMessage || string(data) != "\x01\x00" {
			t.Errorf("audio=%q kind=%d err=%v", data, kind, err)
			return
		}
		kind, data, err = conn.ReadMessage()
		if err != nil || kind != websocket.TextMessage || len(data) != 0 {
			t.Errorf("end=%q kind=%d err=%v", data, kind, err)
			return
		}
		conn.WriteMessage(websocket.TextMessage, []byte(`{"tokens":[{"text":"Hello","is_final":true,"speaker":"1","start_ms":10,"end_ms":100}],"finished":true}`))
	}, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer device-secret" {
			t.Error("wrong backend authentication")
		}
		switch r.URL.Path {
		case "/api/heartbeat":
			if !saved.Load() {
				t.Error("cloud upload preceded local callback")
			}
			var body struct {
				SessionID string         `json:"sessionId"`
				Segments  []cloudSegment `json:"segments"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body.SessionID != "s" || len(body.Segments) != 1 || body.Segments[0].Text != "Hello" || body.Segments[0].Seq != 1 || body.Segments[0].EndMS != 100 {
				t.Errorf("body=%+v", body)
			}
			synced.Store(true)
			w.Write([]byte(`{"ok":true}`))
		case "/api/transcription-sessions/s/end":
			if !synced.Load() {
				t.Error("end preceded sync")
			}
			ended.Store(true)
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected %s", r.URL.Path)
			w.WriteHeader(404)
		}
	})
	audio := make(chan []byte, 1)
	audio <- []byte{1, 0}
	close(audio)
	err := client.Stream(context.Background(), directSession(), audio, func(u transcription.Update) {
		if len(u.Finals) > 0 {
			saved.Store(true)
		}
	})
	if err != nil || !synced.Load() || !ended.Load() {
		t.Fatalf("sync=%v end=%v err=%v", synced.Load(), ended.Load(), err)
	}
}

func TestStreamMeetingRenewsWithoutClosingMeeting(t *testing.T) {
	var grants atomic.Int32
	var audioBytes atomic.Int32
	var synced []cloudSegment
	var ended []string
	var mu sync.Mutex
	oldFinishing := make(chan struct{})
	client := directTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		if _, _, err := conn.ReadMessage(); err != nil {
			t.Error(err)
			return
		}
		for {
			kind, data, err := conn.ReadMessage()
			if err != nil {
				t.Error(err)
				return
			}
			if kind == websocket.TextMessage && len(data) == 0 {
				if key != "second-key" {
					close(oldFinishing)
					time.Sleep(500 * time.Millisecond)
				}
				text := "first"
				if key == "second-key" {
					text = "second"
				}
				_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"tokens":[{"text":"`+text+`","is_final":true,"start_ms":10,"end_ms":20}],"finished":true}`))
				return
			}
			if kind == websocket.BinaryMessage {
				audioBytes.Add(int32(len(data)))
			}
		}
	}, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/transcription-sessions":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["meetingId"] != "m" {
				t.Errorf("renewal meeting ID %q", body["meetingId"])
			}
			grants.Add(1)
			s := directSession()
			s.SessionID = "s2"
			s.TemporaryAPIKey = "second-key"
			_ = json.NewEncoder(w).Encode(s)
		case r.URL.Path == "/api/heartbeat":
			var body struct {
				Segments []cloudSegment `json:"segments"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			synced = append(synced, body.Segments...)
			mu.Unlock()
			_, _ = w.Write([]byte(`{"ok":true}`))
		case strings.HasSuffix(r.URL.Path, "/end"):
			mu.Lock()
			ended = append(ended, r.URL.Path)
			mu.Unlock()
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(404)
		}
	})
	initial := directSession()
	initial.MaxSessionDurationSeconds = 91
	initial.ExpiresAt = time.Now().Add(100 * time.Millisecond)
	audio := make(chan []byte, 1002)
	audio <- make([]byte, 3200)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	switched := make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		finished <- client.StreamMeeting(ctx, initial, audio, nil, func(id string) {
			if id == "s2" {
				close(switched)
			}
		})
	}()
	select {
	case <-oldFinishing:
	case <-ctx.Done():
		t.Fatal("old grant did not finish")
	}
	for range 500 {
		audio <- []byte{3, 0}
	}
	select {
	case <-switched:
	case <-ctx.Done():
		t.Fatal("grant did not renew")
	}
	audio <- []byte{2, 0}
	close(audio)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if grants.Load() != 1 || len(ended) != 1 || ended[0] != "/api/transcription-sessions/s2/end" {
		t.Fatalf("grants=%d ended=%v", grants.Load(), ended)
	}
	if audioBytes.Load() != 4202 {
		t.Fatalf("audio lost during grant handoff: got %d bytes", audioBytes.Load())
	}
	if len(synced) != 2 || synced[0].Seq != 1 || synced[1].Seq != 2 || synced[1].StartMS <= synced[0].EndMS {
		t.Fatalf("transcript sequence or meeting time reset: %+v", synced)
	}
}

func TestStreamMeetingRetriesBackendUploadWhileProviderContinues(t *testing.T) {
	var uploads atomic.Int32
	var audioBytes atomic.Int32
	var ended atomic.Bool
	firstAudio := make(chan struct{})
	client := directTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		if _, _, err := conn.ReadMessage(); err != nil {
			t.Error(err)
			return
		}
		for {
			kind, data, err := conn.ReadMessage()
			if err != nil {
				t.Error(err)
				return
			}
			if kind == websocket.BinaryMessage {
				audioBytes.Add(int32(len(data)))
				if audioBytes.Load() == 2 {
					close(firstAudio)
					_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"tokens":[{"text":"saved","is_final":true,"start_ms":1,"end_ms":2}]}`))
				}
			}
			if kind == websocket.TextMessage && len(data) == 0 {
				_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"finished":true}`))
				return
			}
		}
	}, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/heartbeat":
			var body struct {
				Segments []cloudSegment `json:"segments"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if len(body.Segments) != 1 || body.Segments[0].Seq != 1 {
				t.Errorf("unexpected retry payload %+v", body.Segments)
			}
			if uploads.Add(1) == 1 {
				w.WriteHeader(503)
				return
			}
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/api/transcription-sessions/s/end":
			ended.Store(true)
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(404)
		}
	})
	client.syncInterval = 10 * time.Millisecond
	audio := make(chan []byte, 2)
	audio <- []byte{1, 0}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	finished := make(chan error, 1)
	go func() { finished <- client.StreamMeeting(ctx, directSession(), audio, nil, nil) }()
	select {
	case <-firstAudio:
	case <-ctx.Done():
		t.Fatal("provider did not receive audio")
	}
	deadline := time.After(2 * time.Second)
	for uploads.Load() < 2 {
		select {
		case <-deadline:
			t.Fatal("transcript upload was not retried")
		case <-time.After(5 * time.Millisecond):
		}
	}
	audio <- []byte{2, 0}
	close(audio)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if audioBytes.Load() != 4 || !ended.Load() {
		t.Fatalf("provider audio=%d ended=%v", audioBytes.Load(), ended.Load())
	}
}

func TestStreamMeetingReconnectsAndSendsAudioBufferedDuringOutage(t *testing.T) {
	var connections, renewals atomic.Int32
	var synced []cloudSegment
	var statuses []transcription.ConnectionStatus
	var ended []string
	var mu sync.Mutex
	renewalStarted := make(chan struct{})
	client := directTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		connections.Add(1)
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		if _, _, err := conn.ReadMessage(); err != nil {
			t.Error(err)
			return
		}
		kind, data, err := conn.ReadMessage()
		if err != nil || kind != websocket.BinaryMessage {
			t.Errorf("audio=%q kind=%d err=%v", data, kind, err)
			return
		}
		if key == "temporary-secret" {
			if len(data) != 3200 {
				t.Errorf("first audio bytes=%d", len(data))
			}
			_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"tokens":[{"text":"first","is_final":true,"start_ms":0,"end_ms":20}]}`))
			return
		}
		if key != "renewed-key" || string(data) != "\x02\x00" {
			t.Errorf("renewed key=%q audio=%q", key, data)
			return
		}
		kind, data, err = conn.ReadMessage()
		if err != nil || kind != websocket.TextMessage || len(data) != 0 {
			t.Errorf("end=%q kind=%d err=%v", data, kind, err)
			return
		}
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"tokens":[{"text":"second","is_final":true,"start_ms":0,"end_ms":20}],"finished":true}`))
	}, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/transcription-sessions":
			switch renewals.Add(1) {
			case 1:
				close(renewalStarted)
				w.WriteHeader(503)
				return
			case 2:
				w.WriteHeader(409)
				return
			}
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["meetingId"] != "m" {
				t.Errorf("renewal meeting=%q", body["meetingId"])
			}
			s := directSession()
			s.SessionID = "s2"
			s.TemporaryAPIKey = "renewed-key"
			_ = json.NewEncoder(w).Encode(s)
		case r.URL.Path == "/api/heartbeat":
			var body struct {
				Segments []cloudSegment `json:"segments"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			synced = append(synced, body.Segments...)
			mu.Unlock()
			_, _ = w.Write([]byte(`{"ok":true}`))
		case strings.HasSuffix(r.URL.Path, "/end"):
			mu.Lock()
			ended = append(ended, r.URL.Path)
			mu.Unlock()
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(404)
		}
	})
	audio := make(chan []byte, 2)
	audio <- make([]byte, 3200)
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		finished <- client.StreamMeeting(ctx, directSession(), audio, func(update transcription.Update) {
			if update.ConnectionStatus != transcription.ConnectionUnknown {
				mu.Lock()
				statuses = append(statuses, update.ConnectionStatus)
				mu.Unlock()
			}
		}, nil)
	}()
	select {
	case <-renewalStarted:
	case err := <-finished:
		t.Fatalf("recording stopped on disconnect: %v", err)
	case <-ctx.Done():
		t.Fatal("provider did not request a new grant")
	}
	audio <- []byte{2, 0}
	close(audio)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if connections.Load() != 2 || renewals.Load() != 3 || len(ended) != 1 || ended[0] != "/api/transcription-sessions/s2/end" {
		t.Fatalf("connections=%d renewals=%d ended=%v", connections.Load(), renewals.Load(), ended)
	}
	if len(synced) != 2 || synced[0].Text != "first" || synced[1].Text != "second" || synced[0].Seq != 1 || synced[1].Seq != 2 || synced[1].StartMS != 100 {
		t.Fatalf("lost transcript across reconnect: %+v", synced)
	}
	if len(statuses) != 3 || statuses[0] != transcription.ConnectionConnected || statuses[1] != transcription.ConnectionReconnecting || statuses[2] != transcription.ConnectionConnected {
		t.Fatalf("unexpected connection status sequence: %v", statuses)
	}
}

func TestStreamMeetingStopsWhenRenewalRejectsCredit(t *testing.T) {
	var renewals, ends atomic.Int32
	client := directTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		_, _, _ = conn.ReadMessage()
		_, _, _ = conn.ReadMessage()
	}, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/transcription-sessions" {
			renewals.Add(1)
			w.WriteHeader(402)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/end") {
			ends.Add(1)
			w.WriteHeader(204)
			return
		}
		t.Errorf("unexpected request %s", r.URL.Path)
		w.WriteHeader(404)
	})
	audio := make(chan []byte, 1)
	audio <- []byte{1, 0}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	err := client.StreamMeeting(ctx, directSession(), audio, nil, nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 402 || renewals.Load() != 1 || ends.Load() != 1 {
		t.Fatalf("renewals=%d ends=%d err=%v", renewals.Load(), ends.Load(), err)
	}
}

func TestStreamMeetingCancellationStopsReconnect(t *testing.T) {
	var renewals, ends atomic.Int32
	renewalStarted := make(chan struct{})
	client := directTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		_, _, _ = conn.ReadMessage()
		_, _, _ = conn.ReadMessage()
	}, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/transcription-sessions" {
			if renewals.Add(1) == 1 {
				close(renewalStarted)
			}
			w.WriteHeader(503)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/end") {
			ends.Add(1)
			w.WriteHeader(204)
			return
		}
		t.Errorf("unexpected request %s", r.URL.Path)
		w.WriteHeader(404)
	})
	audio := make(chan []byte, 1)
	audio <- []byte{1, 0}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan error, 1)
	go func() { finished <- client.StreamMeeting(ctx, directSession(), audio, nil, nil) }()
	select {
	case <-renewalStarted:
	case err := <-finished:
		t.Fatalf("recording stopped before cancellation: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("provider did not start reconnecting")
	}
	cancel()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) || ends.Load() != 1 {
			t.Fatalf("ends=%d err=%v", ends.Load(), err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation did not stop reconnect")
	}
}

func TestStreamProviderErrorDoesNotRetry(t *testing.T) {
	var calls, ends atomic.Int32
	client := directTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		conn.ReadMessage()
		conn.WriteMessage(websocket.TextMessage, []byte(`{"error_code":403,"error_type":"temp_api_key_session_expired"}`))
	}, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/end") {
			ends.Add(1)
		}
		w.WriteHeader(204)
	})
	err := client.Stream(context.Background(), directSession(), nil, nil)
	if err == nil || calls.Load() != 1 || ends.Load() != 1 {
		t.Fatalf("calls=%d ends=%d err=%v", calls.Load(), ends.Load(), err)
	}
}

func TestStreamCancellation(t *testing.T) {
	var ends atomic.Int32
	client := directTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}, func(w http.ResponseWriter, r *http.Request) { ends.Add(1); w.WriteHeader(204) })
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	err := client.Stream(ctx, directSession(), nil, nil)
	if !errors.Is(err, context.DeadlineExceeded) || ends.Load() != 1 {
		t.Fatalf("ends=%d err=%v", ends.Load(), err)
	}
}

func TestFailedHandshakeFinalizesAuthorization(t *testing.T) {
	var calls, ends atomic.Int32
	client := directTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(500)
		w.Write([]byte("temporary-secret device-secret"))
	}, func(w http.ResponseWriter, r *http.Request) { ends.Add(1); w.WriteHeader(204) })
	err := client.Stream(context.Background(), directSession(), nil, nil)
	if err == nil || strings.Contains(err.Error(), "secret") || calls.Load() != 1 || ends.Load() != 1 {
		t.Fatalf("calls=%d ends=%d err=%v", calls.Load(), ends.Load(), err)
	}
}

func TestTemporaryAuthorizationValidation(t *testing.T) {
	cases := []struct {
		name   string
		change func(*Session)
	}{
		{"foreign host", func(s *Session) { s.WSURL = "wss://evil.example/transcribe-websocket" }},
		{"query", func(s *Session) { s.WSURL += "?key=leak" }},
		{"plaintext", func(s *Session) { s.WSURL = "ws://stt-rt.soniox.com/transcribe-websocket" }},
		{"device credential fallback", func(s *Session) { s.TemporaryAPIKey = "" }},
		{"expired", func(s *Session) { s.ExpiresAt = time.Now().Add(-time.Second) }},
		{"unbounded expiry", func(s *Session) { s.ExpiresAt = time.Now().Add(2 * time.Hour) }},
		{"unbounded session", func(s *Session) { s.MaxSessionDurationSeconds = 18001 }},
		{"missing cap", func(s *Session) { s.MaxSessionDurationSeconds = 0 }},
		{"unknown model", func(s *Session) { s.Model = "other" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var reached atomic.Bool
			client := directTestClient(t, func(w http.ResponseWriter, r *http.Request) { reached.Store(true) }, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
			s := directSession()
			tc.change(&s)
			if err := client.Stream(context.Background(), s, nil, nil); err == nil || reached.Load() {
				t.Fatalf("unsafe provider reached=%v err=%v", reached.Load(), err)
			}
		})
	}
}

func TestAuthorizeTemporarySession(t *testing.T) {
	client := &Client{BaseURL: "http://localhost:3000", Token: "device-secret", HTTP: &http.Client{Transport: testTransport(func(r *http.Request) (*http.Response, error) {
		recorder := httptest.NewRecorder()
		json.NewEncoder(recorder).Encode(directSession())
		return recorder.Result(), nil
	})}}
	session, err := client.Authorize(context.Background(), "meeting")
	if err != nil || session.TemporaryAPIKey != "temporary-secret" {
		t.Fatalf("err=%v", err)
	}
}

func TestStreamSyncFailureKeepsLocalFinals(t *testing.T) {
	var saved atomic.Bool
	var ends atomic.Int32
	client := directTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		conn.ReadMessage()
		conn.WriteMessage(websocket.TextMessage, []byte(`{"tokens":[{"text":"saved","is_final":true}],"finished":true}`))
	}, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/heartbeat" {
			w.WriteHeader(500)
		} else {
			ends.Add(1)
			w.WriteHeader(204)
		}
	})
	err := client.Stream(context.Background(), directSession(), nil, func(u transcription.Update) {
		if len(u.Finals) > 0 {
			saved.Store(true)
		}
	})
	if err == nil || !strings.Contains(err.Error(), "saved on this device") || !saved.Load() || ends.Load() != 1 {
		t.Fatalf("saved=%v ends=%d err=%v", saved.Load(), ends.Load(), err)
	}
}

func TestBudget(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/heartbeat" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Error("missing device authentication")
		}
		json.NewEncoder(w).Encode(map[string]any{"availableUnits": 20000, "reservedUnits": 10000, "secondsLeft": 7200, "minutesLeft": 120})
	}))
	defer server.Close()
	budget, err := (&Client{BaseURL: server.URL, Token: "secret"}).Budget(context.Background())
	if err != nil || budget.MinutesLeft != 120 || budget.AvailableUnits != 20000 {
		t.Fatalf("budget=%+v err=%v", budget, err)
	}
}

func TestBudgetErrorStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusUnauthorized) }))
	defer server.Close()
	_, err := (&Client{BaseURL: server.URL, Token: "secret"}).Budget(context.Background())
	var api *APIError
	if !errors.As(err, &api) || api.Status != 401 {
		t.Fatalf("got %v", err)
	}
}

func TestBeatInsufficientFunds(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/heartbeat" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		if body["sessionId"] != "s1" {
			t.Errorf("sessionId=%q", body["sessionId"])
		}
		json.NewEncoder(w).Encode(map[string]any{"ok": false, "reason": "insufficient_funds", "secondsLeft": 0, "minutesLeft": 0})
	}))
	defer server.Close()
	beat, err := (&Client{BaseURL: server.URL, Token: "secret"}).Beat(context.Background(), "s1")
	if err != nil || beat.OK || beat.Reason != "insufficient_funds" {
		t.Fatalf("beat=%+v err=%v", beat, err)
	}
}

func TestStreamCancellationInsideFinalCallbackFlushesTranscript(t *testing.T) {
	var synced atomic.Bool
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := directTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		conn.ReadMessage()
		conn.WriteMessage(websocket.TextMessage, []byte(`{"tokens":[{"text":"saved before cancellation","is_final":true}]}`))
		conn.ReadMessage()
	}, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/heartbeat" {
			synced.Store(true)
			w.Write([]byte(`{"ok":true}`))
		} else {
			w.WriteHeader(204)
		}
	})
	err := client.Stream(ctx, directSession(), make(chan []byte), func(u transcription.Update) {
		if len(u.Finals) > 0 {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) || !synced.Load() {
		t.Fatalf("synced=%v err=%v", synced.Load(), err)
	}
}

func TestStreamKeepsPausedProviderAlive(t *testing.T) {
	var received atomic.Bool
	client := directTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		conn.ReadMessage()
		var message map[string]string
		if err := conn.ReadJSON(&message); err != nil {
			t.Error(err)
			return
		}
		if message["type"] != "keepalive" {
			t.Errorf("message=%v", message)
		}
		received.Store(true)
		conn.WriteMessage(websocket.TextMessage, []byte(`{"tokens":[],"finished":true}`))
	}, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	client.keepAliveInterval = time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := client.Stream(ctx, directSession(), make(chan []byte), nil)
	if err != nil || !received.Load() {
		t.Fatalf("received=%v err=%v", received.Load(), err)
	}
}

func TestStreamTranscriptBatchesAreBoundedAndSequenced(t *testing.T) {
	var total atomic.Int32
	client := directTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		conn.ReadMessage()
		tokens := make([]map[string]any, 205)
		for i := range tokens {
			tokens[i] = map[string]any{"text": "a", "speaker": []string{"1", "2"}[i%2], "is_final": true, "start_ms": i, "end_ms": i + 1}
		}
		conn.WriteJSON(map[string]any{"tokens": tokens, "finished": true})
	}, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/heartbeat" {
			w.WriteHeader(204)
			return
		}
		var body struct {
			Segments []cloudSegment `json:"segments"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if len(body.Segments) > 100 || len(body.Segments) == 0 {
			t.Errorf("batch length=%d", len(body.Segments))
		}
		for _, segment := range body.Segments {
			expected := int(total.Add(1))
			if segment.Seq != expected {
				t.Errorf("sequence=%d expected=%d", segment.Seq, expected)
			}
		}
		w.Write([]byte(`{"ok":true}`))
	})
	err := client.Stream(context.Background(), directSession(), nil, nil)
	if err != nil || total.Load() != 205 {
		t.Fatalf("total=%d err=%v", total.Load(), err)
	}
}
