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
// user-facing message.
type APIError struct {
	Status  int
	Message string
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
		return resp.StatusCode, statusError(resp.StatusCode)
	}
	if resp.StatusCode == http.StatusNoContent {
		return resp.StatusCode, nil
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(result); err != nil {
		return resp.StatusCode, errors.New("Titen server returned an invalid response")
	}
	return resp.StatusCode, nil
}

func statusError(status int) error {
	message := fmt.Sprintf("Titen server request failed (HTTP %d)", status)
	switch status {
	case 401, 403:
		message = "Titen sign-in expired or was revoked; sign in again in Settings"
	case 402:
		message = "Titen credit exhausted; top up your account on the website"
	case 400, 404:
		message = "Titen request expired or was rejected; try again"
	}
	return &APIError{Status: status, Message: message}
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
