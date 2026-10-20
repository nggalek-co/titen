package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type transport func(*http.Request) (*http.Response, error)

func (f transport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestRefreshPersistsRotationAndScopesAccount(t *testing.T) {
	original, _ := json.Marshal(token{Refresh: "old-refresh", Expiry: time.Now().Add(-time.Hour)})
	saved := ""
	g := Google{ClientID: "client-1", LoadSecret: func(key string) (string, error) {
		if key != "google-token/client-1" {
			t.Error("unscoped credential")
		}
		return string(original), nil
	}, SaveSecret: func(key, value string) error { saved = value; return nil }, Client: &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
		r.ParseForm()
		if r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("refresh_token") != "old-refresh" || r.Form.Get("client_id") != "client-1" {
			t.Error("invalid refresh request")
		}
		w := httptest.NewRecorder()
		w.WriteString(`{"access_token":"new-access","expires_in":3600}`)
		return w.Result(), nil
	})}}
	access, e := g.Token(context.Background())
	if e != nil || access != "new-access" {
		t.Fatalf("%s %v", access, e)
	}
	var next token
	if e = json.Unmarshal([]byte(saved), &next); e != nil || next.Refresh != "old-refresh" || time.Until(next.Expiry) < 59*time.Minute {
		t.Fatalf("token rotation not persisted %+v %v", next, e)
	}
}
