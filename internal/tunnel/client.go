// Package tunnel connects the agent to a DSUI instance and proxies streams.
package tunnel

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/coder/websocket"

	"github.com/northgraindata/dsui-agent/internal/allowlist"
	"github.com/northgraindata/dsui-agent/internal/config"
	"github.com/northgraindata/dsui-agent/internal/protocol"
)

const (
	minBackoff   = time.Second
	maxBackoff   = time.Minute
	stableAfter  = 30 * time.Second
	pingInterval = 20 * time.Second
)

// Client keeps one tunnel to DSUI alive.
type Client struct {
	cfg     config.Config
	allow   *allowlist.List
	log     *slog.Logger
	version string
	http    *http.Client

	// DialTarget opens the TCP connection to an allowlisted target.
	// Tests replace it; production uses a plain net.Dialer.
	DialTarget func(ctx context.Context, addr string) (net.Conn, error)
}

// New builds a client. The returned error is a configuration problem.
func New(cfg config.Config, log *slog.Logger, version string) (*Client, error) {
	allow, err := allowlist.Parse(cfg.Targets)
	if err != nil {
		return nil, err
	}
	httpClient, err := newHTTPClient(cfg.CAFile)
	if err != nil {
		return nil, err
	}
	c := &Client{cfg: cfg, allow: allow, log: log, version: version, http: httpClient}
	d := &net.Dialer{Timeout: cfg.DialTimeout.Std(), KeepAlive: 30 * time.Second}
	c.DialTarget = func(ctx context.Context, addr string) (net.Conn, error) {
		return d.DialContext(ctx, "tcp", addr)
	}
	return c, nil
}

func newHTTPClient(caFile string) (*http.Client, error) {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = http.ProxyFromEnvironment // honors HTTPS_PROXY / NO_PROXY for the agent's own egress
	if caFile != "" {
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("read CA file: %w", err)
		}
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("CA file %s contains no certificates", caFile)
		}
		tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	return &http.Client{Transport: tr}, nil
}

func (c *Client) dialTarget(ctx context.Context, addr string) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.DialTimeout.Std())
	defer cancel()
	return c.DialTarget(ctx, addr)
}

func classifyDialError(err error) (code, msg string) {
	var ne net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()) {
		return protocol.CodeTimeout, "timed out connecting to the target from the agent"
	}
	return protocol.CodeUnreachable, err.Error()
}

// Run connects, serves and reconnects until ctx is cancelled.
func (c *Client) Run(ctx context.Context) error {
	backoff := minBackoff
	for {
		started := time.Now()
		err := c.runOnce(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if time.Since(started) > stableAfter {
			backoff = minBackoff
		}
		c.logSessionEnd(err)
		wait := jitter(backoff)
		c.log.Info("reconnecting", "in", wait.Round(time.Millisecond).String())
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
		}
		if backoff *= 2; backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

func (c *Client) logSessionEnd(err error) {
	var se *ServerError
	switch {
	case err == nil:
		c.log.Info("tunnel closed")
	case errors.As(err, &se):
		c.log.Error("DSUI refused this agent", "code", se.Code, "message", se.Message,
			"hint", "check the token and that the connector still exists in DSUI")
	default:
		c.log.Warn("tunnel lost", "error", err)
	}
}

func jitter(d time.Duration) time.Duration {
	return time.Duration(float64(d) * (0.8 + 0.4*rand.Float64()))
}

// runOnce opens one session and serves it until it ends.
func (c *Client) runOnce(ctx context.Context) error {
	ws, err := c.dial(ctx)
	if err != nil {
		return err
	}
	sess, sctx := c.newSession(ctx, ws)
	defer sess.cancel(nil)
	defer ws.CloseNow()
	defer sess.closeAll()

	ready, err := sess.handshake()
	if err != nil {
		return err
	}
	c.log.Info("connected to DSUI",
		"observed_ip", ready.ObservedIP, "targets", c.allow.Entries(), "protocol", protocol.Version)

	go sess.heartbeat(c.cfg.HeartbeatEvery.Std())
	go sess.keepAlive(sctx, pingInterval)
	err = sess.readLoop()
	if ctx.Err() != nil {
		_ = ws.Close(websocket.StatusNormalClosure, "agent shutting down")
	}
	return err
}

func (c *Client) dial(ctx context.Context) (*websocket.Conn, error) {
	url, err := c.cfg.TunnelURL()
	if err != nil {
		return nil, err
	}
	dctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	hdr := http.Header{}
	hdr.Set("Authorization", "Bearer "+c.cfg.Token)
	hdr.Set("User-Agent", "dsui-agent/"+c.version)
	hdr.Set("X-DSUI-Protocol", fmt.Sprint(protocol.Version))
	ws, resp, err := websocket.Dial(dctx, url, &websocket.DialOptions{HTTPClient: c.http, HTTPHeader: hdr})
	if err != nil {
		if resp != nil {
			switch resp.StatusCode {
			case http.StatusUnauthorized, http.StatusForbidden:
				return nil, &ServerError{Code: protocol.CodeUnauthorized, Message: fmt.Sprintf("HTTP %d from %s", resp.StatusCode, url)}
			case http.StatusNotFound:
				return nil, fmt.Errorf("HTTP 404 from %s: this DSUI version has no connector support or the URL is wrong", url)
			}
			return nil, fmt.Errorf("connect %s: HTTP %d: %w", url, resp.StatusCode, err)
		}
		return nil, fmt.Errorf("connect %s: %w", url, err)
	}
	return ws, nil
}

// keepAlive pings so idle NATs and proxies keep the connection open and dead
// peers are noticed.
func (s *session) keepAlive(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			err := s.ws.Ping(pctx)
			cancel()
			if err != nil {
				s.cancel(fmt.Errorf("ping: %w", err))
				return
			}
		}
	}
}
