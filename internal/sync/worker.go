package sync

import (
	"context"
	"log/slog"
	"time"
	"titen/internal/storage"
)

type Worker struct {
	Store    *storage.Store
	Docs     *GoogleDocs
	OnStatus func(string)
}

func (w *Worker) status(s string) {
	if w.OnStatus != nil {
		w.OnStatus(s)
	}
}
func Backoff(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	if attempt > 6 {
		attempt = 6
	}
	return time.Second * time.Duration(1<<attempt)
}
func (w *Worker) Once(ctx context.Context) error {
	meetings, e := w.Store.Meetings(ctx, "")
	if e != nil {
		return e
	}
	for _, meeting := range meetings {
		if !meeting.NeedsDocument || meeting.DocumentID != "" || meeting.DocumentRetryAt.After(time.Now()) {
			continue
		}
		id, err := w.Docs.Create(ctx, meeting.Title)
		if err != nil {
			w.status("Google document creation delayed. " + err.Error())
			slog.Warn("Google document creation retry", "meeting_id", meeting.ID, "attempt", meeting.DocumentAttempts+1)
			if err = w.Store.RetryDocument(ctx, meeting.ID, time.Now().Add(Backoff(meeting.DocumentAttempts))); err != nil {
				return err
			}
			continue
		}
		if err = w.Store.SetDocument(ctx, meeting.ID, id); err != nil {
			return err
		}
	}
	ops, e := w.Store.Pending(ctx)
	if e != nil {
		return e
	}
	groups := map[string][]storage.Operation{}
	for _, op := range ops {
		groups[op.DocumentID] = append(groups[op.DocumentID], op)
	}
	for doc, batch := range groups {
		w.status("Syncing")
		e = w.Docs.Append(ctx, doc, batch)
		for _, op := range batch {
			var err error
			if e == nil {
				err = w.Store.Ack(ctx, op.ID)
			} else {
				err = w.Store.Retry(ctx, op.ID, op.Attempts+1, time.Now().Add(Backoff(op.Attempts)))
			}
			if err != nil {
				return err
			}
		}
		if e != nil {
			w.status("Sync delayed. Retrying")
			slog.Warn("Google transcript sync retry", "operation_count", len(batch))
			return e
		}
		w.status("Synced")
		slog.Info("Google transcript synced", "operation_count", len(batch))
	}
	return nil
}
func (w *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(4 * time.Second)
	defer ticker.Stop()
	w.Once(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.Once(ctx)
		}
	}
}
