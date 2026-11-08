package tunnel

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/northgraindata/dsui-agent/internal/config"
	"github.com/northgraindata/dsui-agent/internal/protocol"
)

// fakeDSUI is the server half of the tunnel, reduced to what the tests need.
type fakeDSUI struct {
	srv      *httptest.Server
	token    string
	sessions chan *fakeSession
	hellos   chan protocol.Control

	maxStreams int // advertised in ready when > 0
}

type fakeSession struct {
	ws  *websocket.Conn
	ctx context.Context
}

func newFakeDSUI(t *testing.T, token string) *fakeDSUI {
	t.Helper()
	f := &fakeDSUI{token: token, sessions: make(chan *fakeSession, 8), hellos: make(chan protocol.Control, 8)}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != config.TunnelPath {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+f.token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		ws.SetReadLimit(1 << 20)
		ctx := r.Context()
		_, data, err := ws.Read(ctx)
		if err != nil {
			return
		}
		var hello protocol.Control
		_ = json.Unmarshal(data, &hello)
		f.hellos <- hello
		_ = ws.Write(ctx, websocket.MessageText, protocol.Control{Type: protocol.ControlReady, ObservedIP: "203.0.113.7", MaxStreams: f.maxStreams}.Marshal())
		f.sessions <- &fakeSession{ws: ws, ctx: ctx}
		<-ctx.Done()
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeDSUI) session(t *testing.T) *fakeSession {
	t.Helper()
	select {
	case s := <-f.sessions:
		return s
	case <-time.After(5 * time.Second):
		t.Fatal("agent never connected")
		return nil
	}
}

func (s *fakeSession) send(t *testing.T, fr protocol.Frame) {
	t.Helper()
	if err := s.ws.Write(s.ctx, websocket.MessageBinary, fr.Encode()); err != nil {
		t.Fatal(err)
	}
}

func (s *fakeSession) recv(t *testing.T) protocol.Frame {
	t.Helper()
	ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
	defer cancel()
	for {
		typ, data, err := s.ws.Read(ctx)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if typ != websocket.MessageBinary {
			continue // heartbeats
		}
		fr, err := protocol.Decode(data)
		if err != nil {
			t.Fatal(err)
		}
		return fr
	}
}

func startAgent(t *testing.T, f *fakeDSUI, targets ...string) *Client {
	t.Helper()
	cfg := config.Config{
		URL:            f.srv.URL,
		Token:          f.token,
		Name:           "test-agent",
		Targets:        targets,
		LogFormat:      "text",
		LogLevel:       "debug",
		DialTimeout:    config.Duration(2 * time.Second),
		MaxStreams:     8,
		HeartbeatEvery: config.Duration(50 * time.Millisecond),
	}
	c, err := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), "test")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = c.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return c
}

func echoServer(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = c.Close() }()
				_, _ = io.Copy(c, c)
			}()
		}
	}()
	return ln
}

func TestHandshakeReportsAgentInfo(t *testing.T) {
	f := newFakeDSUI(t, "tok")
	startAgent(t, f, "db.internal:5432")
	f.session(t)
	h := <-f.hellos
	if h.Protocol != protocol.Version || h.Name != "test-agent" || h.OS == "" || len(h.Targets) != 1 || h.Targets[0] != "db.internal:5432" {
		t.Fatalf("unexpected hello: %+v", h)
	}
}

func TestEchoThroughTunnel(t *testing.T) {
	f := newFakeDSUI(t, "tok")
	ln := echoServer(t)
	addr := ln.Addr().String()
	startAgent(t, f, addr)
	s := f.session(t)

	s.send(t, protocol.Frame{Type: protocol.TypeOpen, Stream: 1, Payload: []byte(addr)})
	if fr := s.recv(t); fr.Type != protocol.TypeOpenOK || fr.Stream != 1 {
		t.Fatalf("expected OPEN_OK, got %+v", fr)
	}
	s.send(t, protocol.Frame{Type: protocol.TypeData, Stream: 1, Payload: []byte("ping")})
	fr := s.recv(t)
	if fr.Type != protocol.TypeData || string(fr.Payload) != "ping" {
		t.Fatalf("expected echoed data, got %+v", fr)
	}
	s.send(t, protocol.Frame{Type: protocol.TypeClose, Stream: 1})
	if fr := s.recv(t); fr.Type != protocol.TypeClose {
		t.Fatalf("expected CLOSE after half close, got %+v", fr)
	}
}

func TestTargetOutsideAllowlistIsRefused(t *testing.T) {
	f := newFakeDSUI(t, "tok")
	startAgent(t, f, "allowed.internal:80")
	s := f.session(t)
	s.send(t, protocol.Frame{Type: protocol.TypeOpen, Stream: 1, Payload: []byte("169.254.169.254:80")})
	fr := s.recv(t)
	var body protocol.ErrorBody
	_ = json.Unmarshal(fr.Payload, &body)
	if fr.Type != protocol.TypeOpenErr || body.Code != protocol.CodeNotAllowed {
		t.Fatalf("expected not_allowed, got %+v %+v", fr, body)
	}
}

func TestUnreachableTarget(t *testing.T) {
	f := newFakeDSUI(t, "tok")
	// A port that nothing listens on.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	_ = ln.Close()
	startAgent(t, f, addr)
	s := f.session(t)
	s.send(t, protocol.Frame{Type: protocol.TypeOpen, Stream: 3, Payload: []byte(addr)})
	fr := s.recv(t)
	var body protocol.ErrorBody
	_ = json.Unmarshal(fr.Payload, &body)
	if fr.Type != protocol.TypeOpenErr || body.Code != protocol.CodeUnreachable || fr.Stream != 3 {
		t.Fatalf("expected unreachable, got %+v %+v", fr, body)
	}
}

func TestStreamLimit(t *testing.T) {
	f := newFakeDSUI(t, "tok")
	f.maxStreams = 1
	ln := echoServer(t)
	addr := ln.Addr().String()
	startAgent(t, f, addr)
	s := f.session(t)
	s.send(t, protocol.Frame{Type: protocol.TypeOpen, Stream: 1, Payload: []byte(addr)})
	if fr := s.recv(t); fr.Type != protocol.TypeOpenOK {
		t.Fatalf("expected OPEN_OK, got %+v", fr)
	}
	s.send(t, protocol.Frame{Type: protocol.TypeOpen, Stream: 2, Payload: []byte(addr)})
	fr := s.recv(t)
	var body protocol.ErrorBody
	_ = json.Unmarshal(fr.Payload, &body)
	if fr.Type != protocol.TypeOpenErr || fr.Stream != 2 || body.Code != protocol.CodeTooManyOpen {
		t.Fatalf("expected too_many_streams for stream 2, got %+v %+v", fr, body)
	}
}

func TestLargeTransferRespectsFlowControl(t *testing.T) {
	f := newFakeDSUI(t, "tok")
	// Target that sends 2 MiB and then closes, so the agent must wait for credit.
	payload := make([]byte, 2<<20)
	_, _ = rand.Read(payload)
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		_, _ = c.Write(payload)
		_ = c.Close()
	}()
	addr := ln.Addr().String()
	startAgent(t, f, addr)
	s := f.session(t)
	s.send(t, protocol.Frame{Type: protocol.TypeOpen, Stream: 1, Payload: []byte(addr)})
	if fr := s.recv(t); fr.Type != protocol.TypeOpenOK {
		t.Fatalf("expected OPEN_OK, got %+v", fr)
	}

	var got bytes.Buffer
	var outstanding int64 // bytes received and not yet granted
	for {
		fr := s.recv(t)
		if fr.Type == protocol.TypeClose {
			break
		}
		if fr.Type != protocol.TypeData {
			t.Fatalf("unexpected frame %+v", fr)
		}
		got.Write(fr.Payload)
		outstanding += int64(len(fr.Payload))
		if outstanding > protocol.InitialWindow*2 {
			t.Fatalf("agent exceeded the credit window: %d bytes outstanding", outstanding)
		}
		if outstanding >= protocol.InitialWindow/4 {
			s.send(t, protocol.Frame{Type: protocol.TypeWindow, Stream: 1, Payload: protocol.WindowPayload(uint32(outstanding))})
			outstanding = 0
		}
	}
	if !bytes.Equal(got.Bytes(), payload) {
		t.Fatalf("payload mismatch: got %d bytes, want %d", got.Len(), len(payload))
	}
}

func TestUploadIsGrantedWindowBack(t *testing.T) {
	f := newFakeDSUI(t, "tok")
	var received atomic.Int64
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		n, _ := io.Copy(io.Discard, c)
		received.Store(n)
		_ = c.Close()
	}()
	addr := ln.Addr().String()
	startAgent(t, f, addr)
	s := f.session(t)
	s.send(t, protocol.Frame{Type: protocol.TypeOpen, Stream: 1, Payload: []byte(addr)})
	if fr := s.recv(t); fr.Type != protocol.TypeOpenOK {
		t.Fatalf("expected OPEN_OK, got %+v", fr)
	}

	const total = 1 << 20
	chunk := bytes.Repeat([]byte("x"), protocol.MaxPayload)
	credit := int64(protocol.InitialWindow)
	for sent := int64(0); sent < total; {
		for credit < int64(len(chunk)) {
			fr := s.recv(t)
			if fr.Type != protocol.TypeWindow {
				t.Fatalf("expected WINDOW, got %+v", fr)
			}
			credit += int64(protocol.ParseWindow(fr.Payload))
		}
		s.send(t, protocol.Frame{Type: protocol.TypeData, Stream: 1, Payload: chunk})
		credit -= int64(len(chunk))
		sent += int64(len(chunk))
	}
	s.send(t, protocol.Frame{Type: protocol.TypeClose, Stream: 1})
	deadline := time.Now().Add(5 * time.Second)
	for received.Load() != total && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if received.Load() != total {
		t.Fatalf("target received %d bytes, want %d", received.Load(), total)
	}
}

func TestResetClosesTargetConnection(t *testing.T) {
	f := newFakeDSUI(t, "tok")
	closed := make(chan struct{})
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		_, _ = io.Copy(io.Discard, c)
		close(closed)
	}()
	addr := ln.Addr().String()
	startAgent(t, f, addr)
	s := f.session(t)
	s.send(t, protocol.Frame{Type: protocol.TypeOpen, Stream: 1, Payload: []byte(addr)})
	s.recv(t)
	s.send(t, protocol.Frame{Type: protocol.TypeReset, Stream: 1})
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("target connection stayed open after RESET")
	}
}

func TestReconnectsAfterServerDrops(t *testing.T) {
	f := newFakeDSUI(t, "tok")
	startAgent(t, f, "a.internal:1")
	first := f.session(t)
	_ = first.ws.Close(websocket.StatusGoingAway, "restart")
	second := f.session(t) // blocks until the agent comes back
	if second == nil {
		t.Fatal("no reconnect")
	}
}

func TestWrongTokenIsReportedAsServerError(t *testing.T) {
	f := newFakeDSUI(t, "right")
	c := startAgentNoRun(t, f, "a:1")
	c.cfg.Token = "wrong"
	_, err := c.dial(context.Background())
	var se *ServerError
	ok := errors.As(err, &se)
	if !ok || se.Code != protocol.CodeUnauthorized {
		t.Fatalf("expected unauthorized ServerError, got %T %v", err, err)
	}
}

func TestMissingEndpointHasHelpfulError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	c := startAgentNoRunURL(t, srv.URL)
	_, err := c.dial(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no connector support") {
		t.Fatalf("expected a hint about connector support, got %v", err)
	}
}

func TestCheckReportsTargets(t *testing.T) {
	f := newFakeDSUI(t, "tok")
	ln := echoServer(t)
	c := startAgentNoRun(t, f, ln.Addr().String(), "nothing.invalid:9", "*.corp.local:443")
	go func() { <-f.sessions }()
	res := c.Check(context.Background())
	if len(res) != 4 {
		t.Fatalf("expected 4 results, got %+v", res)
	}
	if !res[0].OK || !res[1].OK || res[2].OK || !res[3].OK {
		t.Fatalf("unexpected check results: %+v", res)
	}
}

func startAgentNoRun(t *testing.T, f *fakeDSUI, targets ...string) *Client {
	t.Helper()
	c, err := New(config.Config{
		URL: f.srv.URL, Token: f.token, Targets: targets,
		DialTimeout: config.Duration(500 * time.Millisecond), MaxStreams: 4,
		HeartbeatEvery: config.Duration(time.Second),
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), "test")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func startAgentNoRunURL(t *testing.T, url string) *Client {
	t.Helper()
	c, err := New(config.Config{
		URL: url, Token: "x", Targets: []string{"a:1"},
		DialTimeout: config.Duration(time.Second), MaxStreams: 4,
		HeartbeatEvery: config.Duration(time.Second),
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), "test")
	if err != nil {
		t.Fatal(err)
	}
	return c
}
