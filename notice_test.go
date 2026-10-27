package main

import (
	"testing"

	"github.com/egoist/mygo/ui"
	"titen/internal/app"
)

func TestErrorNoticeRendersInAlertBand(t *testing.T) {
	d := &desktop{page: "Record", audioPhase: audioReady, snapshot: app.Snapshot{State: app.Idle}, notice: notice{level: noticeError, text: "upload failed"}}
	tt := ui.NewTester(testView(d), 1000, 900)
	if !tt.HasText("upload failed") || !tt.HasText("Dismiss") {
		t.Fatalf("error notice card missing: %v", tt.Texts())
	}
}

func TestInfoNoticeStaysInFooter(t *testing.T) {
	d := &desktop{page: "Record", audioPhase: audioReady, snapshot: app.Snapshot{State: app.Idle}, notice: notice{level: noticeInfo, text: "Done"}}
	tt := ui.NewTester(testView(d), 1000, 900)
	if !tt.HasText("Done") {
		t.Fatalf("info notice missing: %v", tt.Texts())
	}
	if tt.HasText("Dismiss") {
		t.Fatal("info notice rendered a dismiss control")
	}
}

func TestSnapshotErrorStaysDismissedUntilTextChanges(t *testing.T) {
	d := &desktop{page: "Record", audioPhase: audioReady, snapshot: app.Snapshot{State: app.Idle, Error: "disk full"}, errorAck: "disk full"}
	tt := ui.NewTester(testView(d), 1000, 900)
	if tt.HasText("disk full") {
		t.Fatal("acknowledged snapshot error still rendered")
	}
	d.snapshot.Error = "disk full on retry"
	tt.Frame()
	if !tt.HasText("disk full on retry") {
		t.Fatal("changed snapshot error did not render")
	}
	if err := tt.Click("Dismiss"); err != nil {
		t.Fatal(err)
	}
	if tt.HasText("disk full on retry") {
		t.Fatal("dismiss button left the snapshot error visible")
	}
}
