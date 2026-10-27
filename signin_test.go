package main

import (
	"testing"

	"github.com/egoist/mygo/ui"
	"titen/internal/app"
)

func TestNeedsSignIn(t *testing.T) {
	for _, tc := range []struct {
		name     string
		token    string
		endpoint endpointStatus
		want     bool
	}{
		{"signed in and ready", "token", endpointStatus{health: endpointReady}, false},
		{"never signed in", "", endpointStatus{health: endpointAttention, detailKey: "notSignedIn"}, true},
		{"stored token rejected", "stale", endpointStatus{health: endpointAttention, detailKey: "signInRequired"}, true},
		{"no token and server down", "", endpointStatus{health: endpointDown, detailKey: "serverUnreachable"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &desktop{deviceToken: tc.token, endpoint: tc.endpoint}
			if got := d.needsSignIn(); got != tc.want {
				t.Fatalf("needsSignIn = %v", got)
			}
		})
	}
}

func TestRecordViewPromptsSignIn(t *testing.T) {
	for _, tc := range []struct {
		name      string
		token     string
		detailKey string
	}{
		{"not signed in", "", "notSignedIn"},
		{"stored token rejected", "stale", "signInRequired"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &desktop{
				settings:    app.Settings{CaptureMode: "Both", Language: "en"},
				page:        "Record",
				snapshot:    app.Snapshot{State: app.Idle},
				deviceToken: tc.token,
				endpoint:    endpointStatus{health: endpointAttention, detailKey: tc.detailKey},
				microphone:  "System default",
			}
			tt := ui.NewTester(testView(d), 1000, 760)
			if !tt.HasText("Sign in to record") {
				t.Fatalf("sign-in button missing: %v", tt.Texts())
			}
			if tt.HasText("Start recording") {
				t.Fatal("start button rendered while sign-in required")
			}
		})
	}
}

func TestRecordViewSignedInShowsStart(t *testing.T) {
	d := &desktop{
		settings:    app.Settings{CaptureMode: "Both", Language: "en"},
		page:        "Record",
		snapshot:    app.Snapshot{State: app.Idle},
		deviceToken: "token",
		endpoint:    endpointStatus{health: endpointReady, minutesLeft: 30},
		microphone:  "System default",
	}
	tt := ui.NewTester(testView(d), 1000, 760)
	if !tt.HasText("Start recording") {
		t.Fatalf("start button missing: %v", tt.Texts())
	}
	if tt.HasText("Sign in to record") {
		t.Fatal("sign-in button rendered while signed in")
	}
}
