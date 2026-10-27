package main

import (
	"testing"

	"github.com/egoist/mygo/ui"
	"titen/internal/app"
	"titen/internal/saas"
)

func TestRecordingRequiresPassedChecks(t *testing.T) {
	if got := classifyBudget(saas.Budget{MinutesLeft: 9}, nil); got.health != endpointAttention {
		t.Fatal("account below the initial reservation passed the check")
	}
	for _, tc := range []struct {
		name    string
		audio   audioCheckPhase
		account endpointHealth
	}{
		{"unchecked audio", audioUnchecked, endpointReady},
		{"checking audio", audioChecking, endpointReady},
		{"failed audio", audioFailed, endpointReady},
		{"unchecked account", audioReady, endpointUnknown},
		{"account attention", audioReady, endpointAttention},
		{"account unavailable", audioReady, endpointDown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &desktop{audioPhase: tc.audio, endpoint: endpointStatus{health: tc.account}, deviceToken: "token", page: "Record", snapshot: app.Snapshot{State: app.Idle}}
			if d.canRecord() {
				t.Fatal("recording allowed before requirements passed")
			}
			tt := ui.NewTester(testView(d), 1000, 900)
			if err := tt.Click("Start recording"); err != nil {
				t.Fatal(err)
			}
			if d.notice.text != "" || d.busy {
				t.Fatal("disabled recording button ran its action")
			}
			d.start()
			if d.notice.text == "" || d.busy {
				t.Fatal("start method did not reject incomplete requirements")
			}
			d.notice = notice{}
			d.snapshot.State = app.Paused
			d.resume()
			if d.notice.text == "" || d.busy {
				t.Fatal("resume method did not reject incomplete requirements")
			}
		})
	}
	d := &desktop{audioPhase: audioReady, endpoint: endpointStatus{health: endpointReady}}
	if !d.canRecord() {
		t.Fatal("passed requirements did not enable recording")
	}
}
