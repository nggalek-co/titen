package transcription

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/gorilla/websocket"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestAssemblerFinalDedupAndPartialReplacement(t *testing.T) {
	a := Assembler{Offset: 1000}
	raw := []byte(`{"tokens":[{"text":"hello","is_final":true,"start_ms":0,"end_ms":40,"speaker":"1"},{"text":"world","is_final":false}]}`)
	u, _, e := a.Update(raw)
	if e != nil || len(u.Finals) != 1 || u.Partial != "world" || u.Finals[0].StartMS != 1000 {
		t.Fatalf("%+v %v", u, e)
	}
	u, _, e = a.Update(raw)
	if e != nil || len(u.Finals) != 0 || u.Partial != "world" {
		t.Fatal("final tokens duplicated")
	}
}
func TestFinalTokenAssemblyAndBoundedDedup(t *testing.T) {
	a := Assembler{}
	raw := []byte(`{"tokens":[{"text":"Hel","is_final":true,"start_ms":0,"end_ms":10,"speaker":"1"},{"text":"lo ","is_final":true,"start_ms":10,"end_ms":20,"speaker":"1"},{"text":"world","is_final":true,"start_ms":20,"end_ms":30,"speaker":"2"}]}`)
	u, _, err := a.Update(raw)
	if err != nil || len(u.Finals) != 2 || u.Finals[0].Text != "Hello " || u.Finals[1].Text != "world" {
		t.Fatalf("assembly %+v %v", u, err)
	}
	for i := int64(0); i < 300; i++ {
		_, _, err = a.Update([]byte(fmt.Sprintf(`{"tokens":[{"text":"word","is_final":true,"start_ms":%d,"end_ms":%d}]}`, i*1000, i*1000+100)))
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(a.seen) > 62 {
		t.Fatalf("dedup window grew to %d", len(a.seen))
	}
	u, _, err = a.Update(raw)
	if err != nil || len(u.Finals) != 0 {
		t.Fatal("old finalized audio duplicated")
	}
}
func TestWebsocketFlushesFinalTokens(t *testing.T) {
	up := websocket.Upgrader{}
	serverConn, clientConn := net.Pipe()
	listener := &pipeListener{conn: serverConn}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer key" {
			t.Error("missing auth")
		}
		ws, e := up.Upgrade(w, r, nil)
		if e != nil {
			t.Error(e)
			return
		}
		defer ws.Close()
		_, config, e := ws.ReadMessage()
		if e != nil {
			t.Error(e)
			return
		}
		var c map[string]any
		json.Unmarshal(config, &c)
		if c["audio_format"] != "pcm_s16le" {
			t.Error("wrong PCM format")
		}
		kind, data, e := ws.ReadMessage()
		if e != nil || kind != websocket.BinaryMessage || len(data) != 320 {
			t.Error("missing audio")
		}
		kind, data, e = ws.ReadMessage()
		if e != nil || kind != websocket.TextMessage || len(data) != 0 {
			t.Error("missing end text frame")
		}
		ws.WriteMessage(websocket.TextMessage, []byte(`{"tokens":[{"text":"done","is_final":true,"start_ms":0,"end_ms":10}],"finished":true}`))
	})}
	go server.Serve(listener)
	defer server.Close()
	audio := make(chan []byte, 1)
	audio <- make([]byte, 320)
	close(audio)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := Client{APIKey: "key", URL: "ws://mock/", Dialer: &websocket.Dialer{NetDialContext: func(context.Context, string, string) (net.Conn, error) { return clientConn, nil }}}
	finals := 0
	if e := client.Run(ctx, audio, func(u Update) { finals += len(u.Finals) }); e != nil {
		t.Fatal(e)
	}
	if finals != 1 {
		t.Fatalf("finals=%d", finals)
	}
}

type pipeListener struct{ conn net.Conn }

func (p *pipeListener) Accept() (net.Conn, error) {
	if p.conn == nil {
		return nil, io.EOF
	}
	c := p.conn
	p.conn = nil
	return c, nil
}
func (p *pipeListener) Close() error   { return nil }
func (p *pipeListener) Addr() net.Addr { return pipeAddr{} }

type pipeAddr struct{}

func (pipeAddr) Network() string { return "pipe" }
func (pipeAddr) String() string  { return "pipe" }

func TestHandshakeAuthenticationFailureDoesNotRetry(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "denied", http.StatusUnauthorized) })}
	go server.Serve(&pipeListener{conn: serverConn})
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	attempts := 0
	client := Client{APIKey: "invalid", URL: "ws://mock/", Dialer: &websocket.Dialer{NetDialContext: func(context.Context, string, string) (net.Conn, error) { attempts++; return clientConn, nil }}}
	audio := make(chan []byte)
	close(audio)
	err := client.Run(ctx, audio, func(Update) {})
	if err == nil || !strings.Contains(err.Error(), "API key") || attempts != 1 {
		t.Fatalf("error=%v attempts=%d", err, attempts)
	}
}
