package sync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
	"titen/internal/storage"
	"titen/internal/transcript"
)

func TestAmbiguousAppendReconcilesAfterRestart(t *testing.T) {
	ctx := context.Background()
	writes := 0
	marked := false
	marker := ""
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Error("missing auth")
		}
		if r.Method == "GET" {
			ranges := map[string]any{}
			if marked {
				ranges["titen_"+marker] = map[string]any{}
			}
			json.NewEncoder(w).Encode(map[string]any{"revisionId": "rev", "body": map[string]any{"content": []any{map[string]any{"endIndex": 2}}}, "namedRanges": ranges})
			return
		}
		var b struct {
			Requests []map[string]json.RawMessage `json:"requests"`
			Control  map[string]string            `json:"writeControl"`
		}
		json.NewDecoder(r.Body).Decode(&b)
		if b.Control["requiredRevisionId"] != "rev" {
			t.Error("revision control absent")
		}
		writes++
		marked = true
		http.Error(w, "lost response after commit", 503)
	})
	path := filepath.Join(t.TempDir(), "db")
	s, e := storage.Open(path)
	if e != nil {
		t.Fatal(e)
	}
	m, _ := s.CreateMeeting(ctx, "test")
	s.SetDocument(ctx, m.ID, "doc")
	seg := transcript.Segment{MeetingID: m.ID, Text: "hello 🌏", EndMS: 100}
	s.AddSegments(ctx, []transcript.Segment{seg})
	ops, _ := s.Pending(ctx)
	marker = ops[0].ID
	docs := &GoogleDocs{BaseURL: "https://mock", Client: &http.Client{Transport: handlerTransport{handler}}, Token: func(context.Context) (string, error) { return "token", nil }}
	worker := &Worker{Store: s, Docs: docs}
	if e = worker.Once(ctx); e == nil {
		t.Fatal("expected ambiguous failure")
	}
	s.Retry(ctx, marker, 1, time.Unix(0, 0))
	s.Close()
	s, e = storage.Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	worker.Store = s
	if e = worker.Once(ctx); e != nil {
		t.Fatal(e)
	}
	pending, _ := s.Pending(ctx)
	if writes != 1 || len(pending) != 0 {
		t.Fatalf("writes=%d pending=%d", writes, len(pending))
	}
}

type handlerTransport struct{ http.Handler }

func (h handlerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Result(), nil
}

func TestBackoffBounded(t *testing.T) {
	if Backoff(0) != time.Second || Backoff(100) != 64*time.Second {
		t.Fatal("unexpected retry schedule")
	}
}

func TestDocumentCreationRetriesAfterRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "db")
	store, err := storage.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	meeting, err := store.CreateMeeting(ctx, "offline meeting")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.RequestDocument(ctx, meeting.ID); err != nil {
		t.Fatal(err)
	}
	attempts := 0
	docs := &GoogleDocs{BaseURL: "https://mock", Token: func(context.Context) (string, error) { return "token", nil }, Client: &http.Client{Transport: handlerTransport{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			http.Error(w, "unavailable", 503)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"documentId": "created_doc"})
	})}}}
	worker := Worker{Store: store, Docs: docs}
	if err = worker.Once(ctx); err != nil {
		t.Fatal(err)
	}
	meetings, _ := store.Meetings(ctx, "")
	if len(meetings) != 1 || !meetings[0].NeedsDocument || meetings[0].DocumentAttempts != 1 {
		t.Fatalf("missing durable retry: %+v", meetings)
	}
	if err = store.RetryDocument(ctx, meeting.ID, time.Unix(0, 0)); err != nil {
		t.Fatal(err)
	}
	store.Close()
	store, err = storage.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	worker.Store = store
	if err = worker.Once(ctx); err != nil {
		t.Fatal(err)
	}
	meetings, _ = store.Meetings(ctx, "")
	if attempts != 2 || meetings[0].NeedsDocument || meetings[0].DocumentID != "created_doc" {
		t.Fatalf("recovery failed: %+v attempts=%d", meetings, attempts)
	}
	if err = store.SetDocument(ctx, meeting.ID, "different"); err == nil {
		t.Fatal("document reassignment could invalidate checkpoints")
	}
}

func TestDocumentAccessCheckedBeforeAttachment(t *testing.T) {
	ctx := context.Background()
	writable := false
	docs := GoogleDocs{BaseURL: "https://mock", Token: func(context.Context) (string, error) { return "token", nil }, Client: &http.Client{Transport: handlerTransport{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !writable {
			http.Error(w, "not authorized", 403)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"revisionId": "revision"})
	})}}}
	if err := docs.CheckDocument(ctx, "unauthorized"); err == nil {
		t.Fatal("unauthorized document accepted")
	}
	writable = true
	if err := docs.CheckDocument(ctx, "writable"); err != nil {
		t.Fatal(err)
	}
}
