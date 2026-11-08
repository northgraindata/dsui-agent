package tunnel

import (
	"errors"
	"io"
	"net"
	"sync"

	"github.com/northgraindata/dsui-agent/internal/protocol"
)

// stream is one proxied TCP connection. The server opens it; the agent dials
// the target and then pipes bytes in both directions with credit based flow
// control, so one slow target never stalls the other streams on the tunnel.
type stream struct {
	id   uint32
	sess *session

	mu        sync.Mutex
	cond      *sync.Cond
	credit    int64 // bytes the agent may still send to the server
	queue     [][]byte
	queued    int64 // bytes in queue
	consumed  int64 // bytes written to the target and not yet granted back
	srvClosed bool  // server will send no more data
	dead      bool  // aborted: reset, session ended or error
	conn      net.Conn
}

func newStream(id uint32, sess *session) *stream {
	s := &stream{id: id, sess: sess, credit: protocol.InitialWindow}
	s.cond = sync.NewCond(&s.mu)
	return s
}

// run dials the target and pipes data until both directions finish.
func (s *stream) run(host string, port string) {
	defer s.sess.removeStream(s.id)

	conn, err := s.sess.client.dialTarget(s.sess.ctx, net.JoinHostPort(host, port))
	if err != nil {
		code, msg := classifyDialError(err)
		s.sess.sendOpenErr(s.id, code, msg)
		s.abort()
		return
	}

	s.mu.Lock()
	if s.dead {
		s.mu.Unlock()
		_ = conn.Close()
		return
	}
	s.conn = conn
	s.mu.Unlock()

	if err := s.sess.send(protocol.Frame{Type: protocol.TypeOpenOK, Stream: s.id}); err != nil {
		s.abort()
		return
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); s.pumpUp(conn) }()
	go func() { defer wg.Done(); s.pumpDown(conn) }()
	wg.Wait()
	_ = conn.Close()
}

// pumpUp copies target -> server.
func (s *stream) pumpUp(conn net.Conn) {
	buf := make([]byte, protocol.MaxPayload)
	for {
		n, err := conn.Read(buf)
		for off := 0; off < n; {
			chunk, ok := s.takeCredit(n - off)
			if !ok {
				return
			}
			if sendErr := s.sess.send(protocol.Frame{Type: protocol.TypeData, Stream: s.id, Payload: buf[off : off+chunk]}); sendErr != nil {
				s.abort()
				return
			}
			off += chunk
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				_ = s.sess.send(protocol.Frame{Type: protocol.TypeClose, Stream: s.id})
				return
			}
			if s.isDead() {
				return
			}
			_ = s.sess.send(protocol.Frame{Type: protocol.TypeReset, Stream: s.id})
			s.abort()
			return
		}
	}
}

// pumpDown copies server -> target.
func (s *stream) pumpDown(conn net.Conn) {
	for {
		data, closed, ok := s.pop()
		if !ok {
			return
		}
		if len(data) > 0 {
			if _, err := conn.Write(data); err != nil {
				if !s.isDead() {
					_ = s.sess.send(protocol.Frame{Type: protocol.TypeReset, Stream: s.id})
					s.abort()
				}
				return
			}
			s.grant(int64(len(data)))
		}
		if closed {
			if cw, ok := conn.(interface{ CloseWrite() error }); ok {
				_ = cw.CloseWrite()
			}
			return
		}
	}
}

func (s *stream) takeCredit(want int) (int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for s.credit == 0 && !s.dead {
		s.cond.Wait()
	}
	if s.dead {
		return 0, false
	}
	n := int64(want)
	if n > s.credit {
		n = s.credit
	}
	s.credit -= n
	return int(n), true
}

// pop blocks until data is queued, the server closed, or the stream died.
func (s *stream) pop() (data []byte, closed bool, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for len(s.queue) == 0 && !s.srvClosed && !s.dead {
		s.cond.Wait()
	}
	if s.dead {
		return nil, false, false
	}
	if len(s.queue) > 0 {
		data = s.queue[0]
		s.queue = s.queue[1:]
		s.queued -= int64(len(data))
		return data, false, true
	}
	return nil, true, true
}

func (s *stream) grant(n int64) {
	s.mu.Lock()
	s.consumed += n
	var give int64
	if s.consumed >= protocol.InitialWindow/4 {
		give, s.consumed = s.consumed, 0
	}
	s.mu.Unlock()
	if give > 0 {
		_ = s.sess.send(protocol.Frame{Type: protocol.TypeWindow, Stream: s.id, Payload: protocol.WindowPayload(uint32(give))}) //nolint:gosec // G115: bounded by the receive window, see onData
	}
}

func (s *stream) onData(payload []byte) {
	cp := append([]byte(nil), payload...)
	s.mu.Lock()
	if s.dead || s.srvClosed {
		s.mu.Unlock()
		return
	}
	s.queued += int64(len(cp))
	overflow := s.queued > 2*protocol.InitialWindow
	if !overflow {
		s.queue = append(s.queue, cp)
		s.cond.Broadcast()
	}
	s.mu.Unlock()
	if overflow {
		// The server ignored our window: protect memory and drop the stream.
		_ = s.sess.send(protocol.Frame{Type: protocol.TypeReset, Stream: s.id})
		s.abort()
	}
}

func (s *stream) onWindow(n uint32) {
	s.mu.Lock()
	s.credit += int64(n)
	s.cond.Broadcast()
	s.mu.Unlock()
}

func (s *stream) onClose() {
	s.mu.Lock()
	s.srvClosed = true
	s.cond.Broadcast()
	s.mu.Unlock()
}

func (s *stream) isDead() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dead
}

// abort tears the stream down without sending anything.
func (s *stream) abort() {
	s.mu.Lock()
	s.dead = true
	conn := s.conn
	s.cond.Broadcast()
	s.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
}
