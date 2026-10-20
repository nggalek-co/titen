package main

import (
	"image/png"
	"os"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"titen/internal/app"
	"titen/internal/storage"
	"titen/internal/transcript"
)

func saveScreenPNG(t *testing.T, path string, tt *ui.Tester) {
	t.Helper()
	if err := os.MkdirAll("docs/screenshots", 0755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err = png.Encode(f, tt.Image()); err != nil {
		t.Fatal(err)
	}
}

func TestNativeScreens(t *testing.T) {
	d := &desktop{settings: app.Settings{CaptureMode: "Both", Languages: "id,en", AutoSync: true}, page: "Record", snapshot: app.Snapshot{State: app.Idle}, microphone: "System default", syncStatus: "Google Docs sync idle"}
	tt := ui.NewTester(d.view, 1000, 760)
	if !tt.HasText("Start recording") || !tt.HasText("Microphone") || !tt.HasText("System audio") {
		t.Fatalf("recording controls missing: %v", tt.Texts())
	}
	if err := tt.Click("Settings"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("Soniox API key") || !tt.HasText("Connect Google") || !tt.HasText("Test microphone for 10 seconds") {
		t.Fatalf("settings missing: %v", tt.Texts())
	}
	saveScreenPNG(t, "docs/screenshots/native-settings.png", tt)

	d.page = "Record"
	d.snapshot = app.Snapshot{
		State:           app.Recording,
		Duration:        65 * time.Second,
		MicrophoneLevel: 0.42,
		SystemLevel:     0.18,
		Partial:         "Live words",
		Segments: []transcript.Segment{
			{ID: "one", Text: "Final speech", Speaker: "1", StartMS: 1000},
			{ID: "two", Text: "Agreed, let us review the Q3 metrics and capture latency targets.", Speaker: "2", StartMS: 14000},
		},
	}
	tt.Frame()
	if !tt.HasText("Stop recording") || !tt.HasText("Pause") || !tt.HasText("00:01:05") {
		t.Fatalf("active controls missing: %v", tt.Texts())
	}
	saveScreenPNG(t, "docs/screenshots/native-recording.png", tt)

	d.meetings = []storage.Meeting{
		{ID: "m1", Title: "Q3 Product Architecture Sync", CreatedAt: time.Date(2026, 10, 20, 9, 30, 0, 0, time.UTC), DocumentID: "doc-123"},
		{ID: "m2", Title: "Design System Review", CreatedAt: time.Date(2026, 10, 19, 14, 15, 0, 0, time.UTC)},
	}
	d.selected = "m1"
	d.detail = d.snapshot.Segments
	if err := tt.Click("History"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("Copy transcript") || !tt.HasText("Meetings") || !tt.HasText("Q3 Product Architecture Sync") {
		t.Fatal("history controls missing")
	}

	d.page = "Settings"
	tt.SetSize(720, 560)
	tt.Frame()
	tt.SetSize(1000, 760)
	tt.SetDark(true)
	d.page = "Record"
	tt.Frame()
	if !tt.HasText("Live transcript") {
		t.Fatal("dark mode record screen missing")
	}
}

