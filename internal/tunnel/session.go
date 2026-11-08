package tunnel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/northgraindata/dsui-agent/internal/protocol"
)

const (
	writeTimeout = 30 * time.Second
	readyTimeout = 15 * time.Second
	readLimit    = 1 << 20
)

// session is one live WebSocket connection to DSUI.
type session struct {
	client *Client
	ws     *websocket.Conn
	ctx    context.Context
	cancel context.CancelCauseFunc

	mu      sync.Mutex
	streams map[uint32]*stream
	max     int
}

func (c *Client) newSession(ctx context.Context, ws *websocket.Conn) (*session, context.Context) {
	sctx, cancel := context.WithCancelCause(ctx)
	ws.SetReadLimit(readLimit)
	return &session{
		client:  c,
		ws:      ws,
		ctx:     sctx,
		cancel:  cancel,
		streams: map[uint32]*stream{},
		max:     c.cfg.MaxStreams,
	}, sctx
}

func (s *session) send(f protocol.Frame) error {
	ctx, cancel := context.WithTimeout(s.ctx, writeTimeout)
	defer cancel()
	if err := s.ws.Write(ctx, websocket.MessageBinary, f.Encode()); err != nil {
		s.cancel(fmt.Errorf("write: %w", err))
		return err
	}
	return nil
}

func (s *session) sendControl(c protocol.Control) error {
	ctx, cancel := context.WithTimeout(s.ctx, writeTimeout)
	defer cancel()
	if err := s.ws.Write(ctx, websocket.MessageText, c.Marshal()); err != nil {
		s.cancel(fmt.Errorf("write: %w", err))
		return err
	}
	return nil
}

func (s *session) sendOpenErr(id uint32, code, msg string) {
	body, _ := json.Marshal(protocol.ErrorBody{Code: code, Message: msg})
	_ = s.send(protocol.Frame{Type: protocol.TypeOpenErr, Stream: id, Payload: body})
}

func (s *session) removeStream(id uint32) {
	s.mu.Lock()
	delete(s.streams, id)
	s.mu.Unlock()
}

func (s *session) active() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.streams)
}

func (s *session) hello() protocol.Control {
	host, _ := os.Hostname()
	return protocol.Control{
		Type:       protocol.ControlHello,
		Protocol:   protocol.Version,
		Version:    s.client.version,
		Name:       s.client.cfg.Name,
		Hostname:   host,
		OS:         runtime.GOOS,
		Arch:       runtime.GOARCH,
		LocalAddrs: localAddrs(),
		Targets:    s.client.allow.Entries(),
	}
}

func localAddrs() []string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var out []string
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok || ipnet.IP.IsLoopback() || ipnet.IP.IsLinkLocalUnicast() {
			continue
		}
		out = append(out, ipnet.IP.String())
	}
	return out
}

// handshake sends hello and waits for ready.
func (s *session) handshake() (protocol.Control, error) {
	if err := s.sendControl(s.hello()); err != nil {
		return protocol.Control{}, err
	}
	ctx, cancel := context.WithTimeout(s.ctx, readyTimeout)
	defer cancel()
	typ, data, err := s.ws.Read(ctx)
	if err != nil {
		return protocol.Control{}, fmt.Errorf("waiting for ready: %w", err)
	}
	if typ != websocket.MessageText {
		return protocol.Control{}, errors.New("expected a ready message, got binary data")
	}
	var c protocol.Control
	if err := json.Unmarshal(data, &c); err != nil {
		return protocol.Control{}, fmt.Errorf("invalid ready message: %w", err)
	}
	switch c.Type {
	case protocol.ControlReady:
		if c.MaxStreams > 0 && c.MaxStreams < s.max {
			s.max = c.MaxStreams
		}
		return c, nil
	case protocol.ControlError:
		return c, &ServerError{Code: c.Code, Message: c.Message}
	default:
		return c, fmt.Errorf("unexpected %q message during handshake", c.Type)
	}
}

// readLoop dispatches frames until the connection ends.
func (s *session) readLoop() error {
	for {
		typ, data, err := s.ws.Read(s.ctx)
		if err != nil {
			if cause := context.Cause(s.ctx); cause != nil && s.ctx.Err() != nil {
				return cause
			}
			return err
		}
		if typ == websocket.MessageText {
			s.handleControl(data)
			continue
		}
		f, err := protocol.Decode(data)
		if err != nil {
			s.client.log.Warn("dropping malformed frame", "error", err)
			continue
		}
		s.dispatch(f)
	}
}

func (s *session) handleControl(data []byte) {
	var c protocol.Control
	if err := json.Unmarshal(data, &c); err != nil {
		return
	}
	if c.Type == protocol.ControlError {
		s.client.log.Error("server reported an error", "code", c.Code, "message", c.Message)
	}
}

func (s *session) dispatch(f protocol.Frame) {
	if f.Type == protocol.TypeOpen {
		s.handleOpen(f)
		return
	}
	s.mu.Lock()
	st := s.streams[f.Stream]
	s.mu.Unlock()
	if st == nil {
		if f.Type == protocol.TypeData || f.Type == protocol.TypeWindow {
			_ = s.send(protocol.Frame{Type: protocol.TypeReset, Stream: f.Stream})
		}
		return
	}
	switch f.Type {
	case protocol.TypeData:
		st.onData(f.Payload)
	case protocol.TypeWindow:
		st.onWindow(protocol.ParseWindow(f.Payload))
	case protocol.TypeClose:
		st.onClose()
	case protocol.TypeReset:
		st.abort()
	}
}

func (s *session) handleOpen(f protocol.Frame) {
	target := string(f.Payload)
	host, port, ok := s.client.allow.Allows(target)
	if !ok {
		s.client.log.Warn("refused target outside the allowlist", "target", target)
		s.sendOpenErr(f.Stream, protocol.CodeNotAllowed, "target is not on this agent's allowlist")
		return
	}
	s.mu.Lock()
	if _, exists := s.streams[f.Stream]; exists {
		s.mu.Unlock()
		s.client.log.Warn("server reused a stream id", "stream", f.Stream)
		return
	}
	if len(s.streams) >= s.max {
		s.mu.Unlock()
		s.sendOpenErr(f.Stream, protocol.CodeTooManyOpen, "agent stream limit reached")
		return
	}
	st := newStream(f.Stream, s)
	s.streams[f.Stream] = st
	s.mu.Unlock()
	s.client.log.Debug("opening stream", "stream", f.Stream, "target", target)
	go st.run(host, fmt.Sprint(port))
}

// heartbeat reports liveness and current load.
func (s *session) heartbeat(every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-t.C:
			n := s.active()
			if err := s.sendControl(protocol.Control{
				Type:          protocol.ControlHeartbeat,
				Targets:       s.client.allow.Entries(),
				ActiveStreams: &n,
			}); err != nil {
				return
			}
		}
	}
}

// closeAll aborts every stream when the session ends.
func (s *session) closeAll() {
	s.mu.Lock()
	streams := make([]*stream, 0, len(s.streams))
	for _, st := range s.streams {
		streams = append(streams, st)
	}
	s.mu.Unlock()
	for _, st := range streams {
		st.abort()
	}
}

// ServerError is an error the server sent in a control message.
type ServerError struct {
	Code    string
	Message string
}

func (e *ServerError) Error() string {
	return fmt.Sprintf("server rejected the agent (%s): %s", e.Code, e.Message)
}
