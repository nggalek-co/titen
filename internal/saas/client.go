package saas

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

const (
	DefaultEndpoint    = "http://localhost:3000"
	ProductionEndpoint = "https://titen.nggalek.co"
)

type Client struct {
	BaseURL           string
	Token             string
	HTTP              *http.Client
	dialProvider      func(context.Context, string, http.Header) (*websocket.Conn, *http.Response, error)
	keepAliveInterval time.Duration
	syncInterval      time.Duration
}

type DeviceCode struct {
	Code             string    `json:"code"`
	VerifyURL        string    `json:"verifyUrl"`
	ExpiresInSeconds int       `json:"expiresInSeconds"`
	ExpiresAt        time.Time `json:"-"`
}

type Session struct {
	SessionID                 string    `json:"sessionId"`
	MeetingID                 string    `json:"meetingId"`
	ReservationID             string    `json:"reservationId"`
	WSURL                     string    `json:"wsUrl"`
	RateUnitsPerHour          int       `json:"rateUnitsPerHour"`
	TemporaryAPIKey           string    `json:"temporaryApiKey"`
	ExpiresAt                 time.Time `json:"expiresAt"`
	MaxSessionDurationSeconds int       `json:"maxSessionDurationSeconds"`
	Model                     string    `json:"model"`
}

// Budget mirrors GET /api/heartbeat: the device's remaining transcription
// budget, net of all reservations on the account.
type Budget struct {
	AvailableUnits int `json:"availableUnits"`
	ReservedUnits  int `json:"reservedUnits"`
	SecondsLeft    int `json:"secondsLeft"`
	MinutesLeft    int `json:"minutesLeft"`
}

// Heartbeat mirrors POST /api/heartbeat. OK=false means the server finalized
// the session and the caller must stop streaming.
type Heartbeat struct {
	OK          bool   `json:"ok"`
	Reason      string `json:"reason"`
	HeldUnits   int    `json:"heldUnits"`
	SecondsLeft int    `json:"secondsLeft"`
	MinutesLeft int    `json:"minutesLeft"`
}

// APIError carries the HTTP status for UI classification while keeping the
// user-facing message. RetryAfter mirrors the server's Retry-After header
// on 429 and 503 capacity responses.
type APIError struct {
	Status     int
	Message    string
	RetryAfter time.Duration
}

func (e *APIError) Error() string { return e.Message }

// ErrUnavailable marks transport failures: the endpoint could not be reached.
var ErrUnavailable = errors.New("Titen server unavailable; check the endpoint in Settings")

func NormalizeEndpoint(endpoint string) (string, error) {
	if strings.TrimSpace(endpoint) == "" {
		endpoint = DefaultEndpoint
	}
	u, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", errors.New("endpoint must be an HTTP(S) origin without credentials, path, or query")
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	ip := net.ParseIP(u.Hostname())
	local := u.Hostname() == "localhost" || (ip != nil && ip.IsLoopback())
	if u.Scheme != "https" && !(u.Scheme == "http" && local) {
		return "", errors.New("endpoint requires HTTPS, except localhost or loopback development servers")
	}
	if (u.Scheme == "https" && u.Port() == "443") || (u.Scheme == "http" && u.Port() == "80") {
		u.Host = u.Hostname()
		if strings.Contains(u.Host, ":") {
			u.Host = "[" + u.Host + "]"
		}
	}
	u.Path = ""
	return u.String(), nil
}

func TokenKey(endpoint string) string {
	canonical, err := NormalizeEndpoint(endpoint)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256([]byte(canonical))
	return "device-token-" + hex.EncodeToString(sum[:])
}

func (c *Client) request(ctx context.Context, method, path string, body, result any, authorized bool) (int, error) {
	base, err := NormalizeEndpoint(c.BaseURL)
	if err != nil {
		return 0, err
	}
	if authorized && strings.TrimSpace(c.Token) == "" {
		return 0, errors.New("sign in to your Titen account in Settings")
	}
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return 0, errors.New("could not encode Titen request")
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, reader)
	if err != nil {
		return 0, errors.New("could not create Titen request")
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if authorized {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	h := http.Client{Timeout: 20 * time.Second}
	if c.HTTP != nil {
		h = *c.HTTP
	}
	h.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := h.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		return 0, ErrUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, statusError(resp)
	}
	if resp.StatusCode == http.StatusNoContent {
		return resp.StatusCode, nil
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(result); err != nil {
		return resp.StatusCode, errors.New("Titen server returned an invalid response")
	}
	return resp.StatusCode, nil
}

func statusError(resp *http.Response) error {
	status := resp.StatusCode
	message := fmt.Sprintf("Titen server request failed (HTTP %d)", status)
	var retryAfter time.Duration
	if seconds, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && seconds > 0 {
		retryAfter = time.Duration(seconds) * time.Second
	}
	switch status {
	case 401, 403:
		message = "Titen sign-in expired or was revoked; sign in again in Settings"
	case 402:
		message = "Titen credit exhausted; top up your account on the website"
	case 400, 404:
		message = "Titen request expired or was rejected; try again"
	case 429:
		message = "Titen transcription capacity is full; retry later"
		if retryAfter > 0 {
			message = fmt.Sprintf("Titen transcription capacity is full; retry in %s", retryAfter.Round(time.Second))
		}
	case 503:
		message = "Titen transcription is temporarily unavailable; retry later"
		if retryAfter > 0 {
			message = fmt.Sprintf("Titen transcription is temporarily unavailable; retry in %s", retryAfter.Round(time.Second))
		}
	}
	return &APIError{Status: status, Message: message, RetryAfter: retryAfter}
}

func (c *Client) BeginDevice(ctx context.Context, name string) (DeviceCode, error) {
	var code DeviceCode
	_, err := c.request(ctx, http.MethodPost, "/api/device/codes", map[string]string{"deviceName": name}, &code, false)
	if err != nil {
		return code, err
	}
	if code.Code == "" || code.ExpiresInSeconds <= 0 || code.ExpiresInSeconds > 86400 {
		return DeviceCode{}, errors.New("Titen server returned an invalid device code")
	}
	if err = c.validateURL(code.VerifyURL); err != nil {
		return DeviceCode{}, err
	}
	code.ExpiresAt = time.Now().Add(time.Duration(code.ExpiresInSeconds) * time.Second)
	return code, nil
}

func (c *Client) WaitDevice(ctx context.Context, code DeviceCode) (string, error) {
	expires := code.ExpiresAt
	if expires.IsZero() {
		expires = time.Now().Add(time.Duration(code.ExpiresInSeconds) * time.Second)
	}
	ctx, cancel := context.WithDeadline(ctx, expires)
	defer cancel()
	for {
		var response struct {
			Token   string `json:"token"`
			Pending bool   `json:"pending"`
		}
		status, err := c.request(ctx, http.MethodPost, "/api/device/token", map[string]string{"code": code.Code}, &response, false)
		if err != nil {
			return "", err
		}
		if status == http.StatusOK && response.Token != "" {
			return response.Token, nil
		}
		if status != http.StatusAccepted || !response.Pending {
			return "", errors.New("Titen server returned an invalid sign-in response")
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return "", ctx.Err()
		case <-timer.C:
		}
	}
}

func (c *Client) Authorize(ctx context.Context, clientTag string) (Session, error) {
	return c.authorize(ctx, clientTag, "")
}

func (c *Client) AuthorizeRenewal(ctx context.Context, meetingID string) (Session, error) {
	if meetingID == "" {
		return Session{}, errors.New("Titen meeting is missing for transcription renewal")
	}
	return c.authorize(ctx, "", meetingID)
}

func (c *Client) authorize(ctx context.Context, clientTag, meetingID string) (Session, error) {
	var session Session
	body := map[string]string{"clientTag": clientTag}
	if meetingID != "" {
		body["meetingId"] = meetingID
	}
	_, err := c.request(ctx, http.MethodPost, "/api/transcription-sessions", body, &session, true)
	if err != nil {
		return session, err
	}
	if session.SessionID == "" || session.MeetingID == "" || session.ReservationID == "" || (meetingID != "" && session.MeetingID != meetingID) {
		return Session{}, errors.New("Titen server returned an invalid transcription session")
	}
	if err = session.validateProvider(); err != nil {
		if meetingID == "" {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			_ = c.End(cleanupCtx, session)
			cancel()
		}
		return Session{}, err
	}
	return session, nil
}

func (s Session) validateProvider() error {
	if s.WSURL != "wss://stt-rt.soniox.com/transcribe-websocket" {
		return errors.New("Titen server returned an untrusted Soniox stream URL; update the backend")
	}
	if strings.TrimSpace(s.TemporaryAPIKey) == "" || s.Model != "stt-rt-v5" || s.MaxSessionDurationSeconds < 1 || s.MaxSessionDurationSeconds > 18000 || !s.ExpiresAt.After(time.Now()) || s.ExpiresAt.After(time.Now().Add(time.Hour)) {
		return errors.New("Titen server returned invalid or expired temporary Soniox credentials; start a new session")
	}
	return nil
}

func (c *Client) Budget(ctx context.Context) (Budget, error) {
	var budget Budget
	_, err := c.request(ctx, http.MethodGet, "/api/heartbeat", nil, &budget, true)
	return budget, err
}

func (c *Client) Beat(ctx context.Context, sessionID string) (Heartbeat, error) {
	var beat Heartbeat
	_, err := c.request(ctx, http.MethodPost, "/api/heartbeat", map[string]string{"sessionId": sessionID}, &beat, true)
	return beat, err
}

func (c *Client) End(ctx context.Context, session Session) error {
	if session.SessionID == "" {
		return errors.New("Titen transcription session is missing")
	}
	var result json.RawMessage
	_, err := c.request(ctx, http.MethodPost, "/api/transcription-sessions/"+url.PathEscape(session.SessionID)+"/end", map[string]string{}, &result, true)
	return err
}

// ReportProviderError forwards the provider's stable error_type and
// request_id to the backend. The Worker cannot see the direct WebSocket,
// so these reports are its only signal about provider-side failures.
func (c *Client) ReportProviderError(ctx context.Context, sessionID, errorType, requestID string) error {
	if sessionID == "" || errorType == "" {
		return nil
	}
	var result json.RawMessage
	_, err := c.request(ctx, http.MethodPost, "/api/transcription-sessions/"+url.PathEscape(sessionID)+"/provider-error", map[string]string{
		"errorType": errorType,
		"requestId": requestID,
	}, &result, true)
	return err
}

// ChunkRegistration mirrors POST /api/audio-chunks: a presigned R2 PUT URL
// for one registered WAV chunk.
type ChunkRegistration struct {
	ChunkID   string `json:"chunkId"`
	UploadURL string `json:"uploadUrl"`
	R2Key     string `json:"r2Key"`
	Stored    bool   `json:"stored"`
}

// CreateCloudMeeting opens a cloud meeting for a locally saved recording
// that never obtained a realtime session (capacity was full at start).
func (c *Client) CreateCloudMeeting(ctx context.Context, title string) (string, error) {
	var result struct {
		MeetingID string `json:"meetingId"`
	}
	_, err := c.request(ctx, http.MethodPost, "/api/meetings", map[string]string{"title": title}, &result, true)
	if err != nil {
		return "", err
	}
	if result.MeetingID == "" {
		return "", errors.New("Titen server returned an invalid meeting")
	}
	return result.MeetingID, nil
}

func (c *Client) RegisterChunk(ctx context.Context, chunk struct {
	MeetingID  string `json:"meetingId"`
	ChunkIndex int    `json:"chunkIndex"`
	StartMS    int64  `json:"startMs"`
	DurationMS int64  `json:"durationMs"`
	Format     string `json:"format"`
	Checksum   string `json:"checksum"`
	SizeBytes  int64  `json:"sizeBytes"`
	Consent    bool   `json:"consent"`
}) (ChunkRegistration, error) {
	var registration ChunkRegistration
	_, err := c.request(ctx, http.MethodPost, "/api/audio-chunks", chunk, &registration, true)
	if err != nil {
		return registration, err
	}
	if registration.ChunkID == "" || registration.UploadURL == "" {
		return registration, errors.New("Titen server returned an invalid chunk upload URL")
	}
	return registration, nil
}

func (c *Client) MarkChunkUploaded(ctx context.Context, chunkID string) error {
	var result json.RawMessage
	_, err := c.request(ctx, http.MethodPost, "/api/audio-chunks/"+url.PathEscape(chunkID)+"/uploaded", map[string]string{}, &result, true)
	return err
}

// UploadAudio PUTs bytes straight to the presigned object URL — the Titen
// token does not go there and the body is not logged.
func (c *Client) UploadAudio(ctx context.Context, uploadURL string, data []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, uploadURL, bytes.NewReader(data))
	if err != nil {
		return errors.New("could not create audio upload request")
	}
	h := http.Client{Timeout: 5 * time.Minute}
	if c.HTTP != nil {
		h = *c.HTTP
		h.Timeout = 5 * time.Minute
	}
	resp, err := h.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("audio upload rejected (HTTP %d)", resp.StatusCode)
	}
	return nil
}

func (c *Client) validateURL(raw string) error {
	base, err := NormalizeEndpoint(c.BaseURL)
	if err != nil {
		return err
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Fragment != "" {
		return errors.New("Titen server returned an unsafe URL")
	}
	origin, err := NormalizeEndpoint(u.Scheme + "://" + u.Host)
	if err != nil || origin != base {
		return errors.New("Titen server returned a URL for a different endpoint")
	}
	return nil
}
