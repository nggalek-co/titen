package main

import (
	"image/png"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"titen/internal/app"
	"titen/internal/saas"
	"titen/internal/storage"
	"titen/internal/transcript"
	"titen/locales"
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

// testView renders titlebar, sidebar and content together so the tester sees
// the same widgets the shell window shows.
func testView(d *desktop) func(*ui.Context) {
	return func(c *ui.Context) {
		ui.Column(c).Fill().Children(func() {
			d.titlebar(c)
			ui.Row(c).Grow(1).MinHeight(0).Children(func() {
				ui.Column(c).Width(220).Shrink(0).Children(func() { d.nav(c) })
				ui.Column(c).Grow(1).MinWidth(0).Children(func() { d.view(c) })
			})
		})
	}
}

func TestNativeScreens(t *testing.T) {
	d := &desktop{settings: app.Settings{ServerEndpoint: "http://localhost:3000", CaptureMode: "Both", Language: "en"}, deviceToken: "token", page: "Record", snapshot: app.Snapshot{State: app.Idle}, microphone: "System default"}
	tt := ui.NewTester(testView(d), 1000, 760)
	if !tt.HasText("Start recording") || !tt.HasText("Microphone") || !tt.HasText("System audio") {
		t.Fatalf("recording controls missing: %v", tt.Texts())
	}
	if err := tt.Click("Settings"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("Server endpoint") || !tt.HasText("Sign in with browser") || !tt.HasText("Test microphone for 10 seconds") || !tt.HasText("Language") {
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

	d.endpoint = endpointStatus{health: endpointReady, minutesLeft: 95}
	d.page = "Record"
	tt.Frame()
	if !tt.HasText("Ready · 1h 35m left") {
		t.Fatalf("endpoint pill missing: %v", tt.Texts())
	}
	d.endpoint = endpointStatus{health: endpointAttention, detailKey: "outOfCredit"}
	tt.Frame()
	if !tt.HasText("Out of credit") {
		t.Fatalf("attention pill missing: %v", tt.Texts())
	}
	d.endpoint = endpointStatus{health: endpointDown, detailKey: "serverUnreachable"}
	tt.Frame()
	if !tt.HasText("Server unreachable") {
		t.Fatalf("down pill missing: %v", tt.Texts())
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

func TestIndonesianCatalog(t *testing.T) {
	if got := locales.Catalog.Format("id", "navRecord", nil); got != "Rekam" {
		t.Fatalf("navRecord in id: %q", got)
	}
	if got := locales.Catalog.Format("id", "segmentsBadge", map[string]any{"count": 3}); got != "3 segmen" {
		t.Fatalf("segmentsBadge in id: %q", got)
	}
	if got := locales.Catalog.Format("en", "segmentsBadge", map[string]any{"count": 1}); got != "1 segment" {
		t.Fatalf("segmentsBadge in en: %q", got)
	}
	if got := locales.Catalog.Format("id", "tipPrefix", map[string]any{"state": "Siap"}); got != "Titen · Siap" {
		t.Fatalf("tipPrefix in id: %q", got)
	}
}

func TestClassifyBudget(t *testing.T) {
	if got := classifyBudget(saas.Budget{MinutesLeft: 10}, nil); got.health != endpointReady || got.minutesLeft != 10 {
		t.Fatalf("ready: %+v", got)
	}
	if got := classifyBudget(saas.Budget{MinutesLeft: 0}, nil); got.health != endpointAttention || got.detailKey != "outOfCredit" {
		t.Fatalf("no credit: %+v", got)
	}
	if got := classifyBudget(saas.Budget{}, saas.ErrUnavailable); got.health != endpointDown {
		t.Fatalf("down: %+v", got)
	}
	if got := classifyBudget(saas.Budget{}, &saas.APIError{Status: 401}); got.health != endpointAttention || got.detailKey != "signInRequired" {
		t.Fatalf("unauthorized: %+v", got)
	}
}

func TestTranscriptRowsJoinProviderBatches(t *testing.T) {
	segments := []transcript.Segment{
		{ID: "a", Speaker: "1", Text: "salam sej", StartMS: 4000, EndMS: 5000},
		{ID: "b", Speaker: "1", Text: "ahtera bagi kita.", StartMS: 5000, EndMS: 6000},
		{ID: "c", Speaker: "2", Text: "Amin.", StartMS: 6200, EndMS: 6500},
	}
	rows := transcriptRows(segments)
	if len(rows) != 2 || rows[0].Text != "salam sejahtera bagi kita." || rows[0].ID != "a" || rows[1].Text != "Amin." {
		t.Fatalf("joined rows = %+v", rows)
	}
	if segments[0].Text != "salam sej" {
		t.Fatal("source segments changed")
	}
}

func TestTranscriptRowsKeepSpeakerTurnAndParagraphs(t *testing.T) {
	segments := []transcript.Segment{
		{ID: "a", Speaker: "1", Text: strings.Repeat("Kalimat lengkap. ", 18) + "Provin", StartMS: 0, EndMS: 15_000},
		{ID: "b", Speaker: "1", Text: "si dan kabupaten.", StartMS: 16_000, EndMS: 20_000},
		{ID: "c", Speaker: "1", Text: " Laporan berikutnya.", StartMS: 24_000, EndMS: 25_000},
		{ID: "d", Speaker: "1", Text: "Setelah jeda panjang.", StartMS: 46_000, EndMS: 48_000},
	}
	rows := transcriptRows(segments)
	if len(rows) != 2 || !strings.Contains(rows[0].Text, "Provinsi dan kabupaten.\n\nLaporan berikutnya.") || rows[1].ID != "d" {
		t.Fatalf("speaker turns = %+v", rows)
	}
}
