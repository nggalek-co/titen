package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"
	"titen/internal/transcript"
)

func TestDurableTranscriptAndOutbox(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "meetings.sqlite")
	s, e := Open(path)
	if e != nil {
		t.Fatal(e)
	}
	m, e := s.CreateMeeting(ctx, "Standup")
	if e != nil {
		t.Fatal(e)
	}
	seg := transcript.Segment{MeetingID: m.ID, Speaker: "1", Text: "Hello", StartMS: 10, EndMS: 100}
	if e = s.AddSegments(ctx, []transcript.Segment{seg, seg}); e != nil {
		t.Fatal(e)
	}
	if e = s.SetDocument(ctx, m.ID, "doc"); e != nil {
		t.Fatal(e)
	}
	s.Close()
	s, e = Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	segs, e := s.Segments(ctx, m.ID)
	if e != nil || len(segs) != 1 {
		t.Fatalf("segments=%v error=%v", segs, e)
	}
	ops, e := s.Pending(ctx)
	if e != nil || len(ops) != 1 || ops[0].DocumentID != "doc" {
		t.Fatalf("outbox=%v error=%v", ops, e)
	}
	if e = s.Ack(ctx, ops[0].ID); e != nil {
		t.Fatal(e)
	}
	ops, e = s.Pending(ctx)
	if e != nil || len(ops) != 0 {
		t.Fatalf("ack failed %v %v", ops, e)
	}
	meetings, e := s.Meetings(ctx, "Hello")
	if e != nil || len(meetings) != 1 {
		t.Fatalf("search %v %v", meetings, e)
	}
}
func TestRetryPreservesOrderAndMeetingIdentity(t *testing.T) {
	ctx := context.Background()
	s, e := Open(":memory:")
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	a, _ := s.CreateMeeting(ctx, "A")
	b, _ := s.CreateMeeting(ctx, "B")
	s.SetDocument(ctx, a.ID, "doc-a")
	s.SetDocument(ctx, b.ID, "doc-b")
	first := transcript.Segment{MeetingID: a.ID, Text: "hello", EndMS: 10}
	if e = s.AddSegments(ctx, []transcript.Segment{first, {MeetingID: a.ID, Text: " world", StartMS: 10, EndMS: 20}, {MeetingID: b.ID, Text: "hello", EndMS: 10}}); e != nil {
		t.Fatal(e)
	}
	ops, e := s.Pending(ctx)
	if e != nil || len(ops) != 3 {
		t.Fatalf("ops=%v err=%v", ops, e)
	}
	if e = s.Retry(ctx, first.Key(), 1, time.Now().Add(time.Minute)); e != nil {
		t.Fatal(e)
	}
	ops, e = s.Pending(ctx)
	if e != nil || len(ops) != 1 || ops[0].MeetingID != b.ID {
		t.Fatalf("later text overtook retry %v %v", ops, e)
	}
}
func TestSegmentBatchRollback(t *testing.T) {
	ctx := context.Background()
	s, e := Open(":memory:")
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	m, _ := s.CreateMeeting(ctx, "test")
	e = s.AddSegments(ctx, []transcript.Segment{{MeetingID: m.ID, Text: "saved?"}, {Text: "invalid"}})
	if e == nil {
		t.Fatal("invalid segment accepted")
	}
	rows, e := s.Segments(ctx, m.ID)
	if e != nil || len(rows) != 0 {
		t.Fatalf("partial commit %v %v", rows, e)
	}
}
