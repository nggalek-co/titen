package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"titen/internal/storage"
	"unicode/utf16"
)

type GoogleDocs struct {
	Token   func(context.Context) (string, error)
	Client  *http.Client
	BaseURL string
}

func (g *GoogleDocs) request(ctx context.Context, method, path string, body, out any) error {
	var b bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&b).Encode(body); err != nil {
			return err
		}
	}
	base := g.BaseURL
	if base == "" {
		base = "https://docs.googleapis.com/v1"
	}
	req, e := http.NewRequestWithContext(ctx, method, strings.TrimRight(base, "/")+path, &b)
	if e != nil {
		return e
	}
	if g.Token == nil {
		return fmt.Errorf("Google sign-in required")
	}
	token, e := g.Token(ctx)
	if e != nil {
		return e
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	client := g.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, e := client.Do(req)
	if e != nil {
		return e
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Google Docs HTTP %d", resp.StatusCode)
	}
	if out != nil {
		return json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(out)
	}
	return nil
}
func (g *GoogleDocs) Create(ctx context.Context, title string) (string, error) {
	var result struct {
		ID string `json:"documentId"`
	}
	err := g.request(ctx, "POST", "/documents", map[string]string{"title": title}, &result)
	return result.ID, err
}

type document struct {
	Revision    string                     `json:"revisionId"`
	NamedRanges map[string]json.RawMessage `json:"namedRanges"`
	Body        struct {
		Content []struct {
			End int `json:"endIndex"`
		} `json:"content"`
	} `json:"body"`
}

func (g *GoogleDocs) Append(ctx context.Context, doc string, ops []storage.Operation) error {
	var d document
	path := "/documents/" + url.PathEscape(doc)
	if e := g.request(ctx, "GET", path, nil, &d); e != nil {
		return e
	}
	index := 1
	for _, v := range d.Body.Content {
		if v.End > index {
			index = v.End - 1
		}
	}
	requests := []any{}
	for _, op := range ops {
		marker := "titen_" + op.ID
		if _, ok := d.NamedRanges[marker]; ok {
			continue
		}
		size := len(utf16.Encode([]rune(op.Text)))
		if size == 0 {
			continue
		}
		requests = append(requests, map[string]any{"insertText": map[string]any{"location": map[string]int{"index": index}, "text": op.Text}}, map[string]any{"createNamedRange": map[string]any{"name": marker, "range": map[string]int{"startIndex": index, "endIndex": index + size}}})
		index += size
	}
	if len(requests) == 0 {
		return nil
	}
	if d.Revision == "" {
		return fmt.Errorf("Google document has no writable revision")
	}
	return g.request(ctx, "POST", path+":batchUpdate", map[string]any{"requests": requests, "writeControl": map[string]string{"requiredRevisionId": d.Revision}}, nil)
}

func (g *GoogleDocs) CheckDocument(ctx context.Context, id string) error {
	var d document
	if err := g.request(ctx, "GET", "/documents/"+url.PathEscape(id), nil, &d); err != nil {
		return err
	}
	if d.Revision == "" {
		return fmt.Errorf("this Google document is not writable by the connected account")
	}
	return nil
}
