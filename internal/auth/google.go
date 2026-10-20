package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type Google struct {
	ClientID, ClientSecret string
	ClientSecretLookup     func() (string, error)
	OpenBrowser            func(string) error
	Client                 *http.Client
	TokenURL, AuthURL      string
	SaveSecret             func(string, string) error
	LoadSecret             func(string) (string, error)
	mu                     sync.Mutex
}

func (g *Google) account() string { return "google-token/" + g.ClientID }
func (g *Google) save(value string) error {
	if g.SaveSecret != nil {
		return g.SaveSecret(g.account(), value)
	}
	return SetSecret(g.account(), value)
}
func (g *Google) load() (string, error) {
	if g.LoadSecret != nil {
		return g.LoadSecret(g.account())
	}
	return GetSecret(g.account())
}

type token struct {
	Access    string    `json:"access_token"`
	Refresh   string    `json:"refresh_token"`
	ExpiresIn int       `json:"expires_in"`
	Expiry    time.Time `json:"expiry"`
}

func random() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
func (g *Google) exchange(ctx context.Context, values url.Values) (token, error) {
	values.Set("client_id", g.ClientID)
	secret := g.ClientSecret
	if secret == "" && g.ClientSecretLookup != nil {
		var err error
		secret, err = g.ClientSecretLookup()
		if err != nil {
			return token{}, fmt.Errorf("Google desktop OAuth client secret is missing or inaccessible; save it in Settings: %w", err)
		}
	}
	if secret != "" {
		values.Set("client_secret", secret)
	}
	endpoint := g.TokenURL
	if endpoint == "" {
		endpoint = "https://oauth2.googleapis.com/token"
	}
	req, e := http.NewRequestWithContext(ctx, "POST", endpoint, strings.NewReader(values.Encode()))
	if e != nil {
		return token{}, e
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := g.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, e := client.Do(req)
	if e != nil {
		return token{}, e
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return token{}, fmt.Errorf("Google OAuth HTTP %d", resp.StatusCode)
	}
	var t token
	if e = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&t); e != nil {
		return t, e
	}
	if t.Access == "" {
		return t, fmt.Errorf("Google returned no access token")
	}
	t.Expiry = time.Now().Add(time.Duration(t.ExpiresIn) * time.Second)
	return t, nil
}
func (g *Google) Connect(ctx context.Context) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.ClientID == "" || g.OpenBrowser == nil {
		return fmt.Errorf("configure Google desktop OAuth client and browser launcher")
	}
	listener, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		return e
	}
	defer listener.Close()
	state, verifier := random(), random()
	hash := sha256.Sum256([]byte(verifier))
	redirect := "http://" + listener.Addr().String() + "/callback"
	result := make(chan string, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("state") != state {
			http.Error(w, "Invalid OAuth state", 400)
			return
		}
		code := r.URL.Query().Get("code")
		if code == "" {
			http.Error(w, "Authorization denied", 400)
			select {
			case result <- "":
			default:
			}
			return
		}
		select {
		case result <- code:
		default:
		}
		fmt.Fprint(w, "Authorization received. Return to Titen to check connection status.")
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go server.Serve(listener)
	defer server.Close()
	v := url.Values{"client_id": {g.ClientID}, "redirect_uri": {redirect}, "response_type": {"code"}, "scope": {"https://www.googleapis.com/auth/drive.file"}, "access_type": {"offline"}, "prompt": {"consent"}, "state": {state}, "code_challenge": {base64.RawURLEncoding.EncodeToString(hash[:])}, "code_challenge_method": {"S256"}}
	endpoint := g.AuthURL
	if endpoint == "" {
		endpoint = "https://accounts.google.com/o/oauth2/v2/auth"
	}
	if e = g.OpenBrowser(endpoint + "?" + v.Encode()); e != nil {
		return e
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case code := <-result:
		if code == "" {
			return fmt.Errorf("Google authorization denied")
		}
		t, e := g.exchange(ctx, url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirect}, "code_verifier": {verifier}})
		if e != nil {
			return e
		}
		b, _ := json.Marshal(t)
		return g.save(string(b))
	}
}
func (g *Google) Token(ctx context.Context) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	raw, e := g.load()
	if e != nil {
		return "", e
	}
	var t token
	if e = json.Unmarshal([]byte(raw), &t); e != nil {
		return "", e
	}
	if time.Until(t.Expiry) > time.Minute {
		return t.Access, nil
	}
	if t.Refresh == "" {
		return "", fmt.Errorf("Google sign-in required")
	}
	next, e := g.exchange(ctx, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {t.Refresh}})
	if e != nil {
		return "", e
	}
	if next.Refresh == "" {
		next.Refresh = t.Refresh
	}
	b, _ := json.Marshal(next)
	if e = g.save(string(b)); e != nil {
		return "", e
	}
	return next.Access, nil
}
