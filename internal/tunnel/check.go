package tunnel

import (
	"context"
	"strings"

	"github.com/coder/websocket"
)

// CheckResult is one line of the `dsui-agent check` report.
type CheckResult struct {
	Name   string
	OK     bool
	Detail string
}

// Check verifies the configuration end to end without serving traffic:
// it reaches DSUI, authenticates, and tries every concrete allowlist target.
func (c *Client) Check(ctx context.Context) []CheckResult {
	var out []CheckResult

	ws, err := c.dial(ctx)
	if err != nil {
		out = append(out, CheckResult{Name: "DSUI reachable and token accepted", Detail: err.Error()})
	} else {
		sess, _ := c.newSession(ctx, ws)
		ready, hsErr := sess.handshake()
		if hsErr != nil {
			out = append(out, CheckResult{Name: "DSUI reachable and token accepted", Detail: hsErr.Error()})
		} else {
			detail := "connected"
			if ready.ObservedIP != "" {
				detail += ", DSUI sees this agent as " + ready.ObservedIP
			}
			out = append(out, CheckResult{Name: "DSUI reachable and token accepted", OK: true, Detail: detail})
		}
		sess.cancel(nil)
		_ = ws.Close(websocket.StatusNormalClosure, "check complete")
	}

	for _, entry := range c.allow.Entries() {
		if strings.Contains(entry, "*") || strings.Contains(entry[:strings.LastIndex(entry, ":")], "/") {
			out = append(out, CheckResult{Name: "target " + entry, OK: true, Detail: "pattern, checked when DSUI opens a concrete target"})
			continue
		}
		conn, dialErr := c.dialTarget(ctx, entry)
		if dialErr != nil {
			out = append(out, CheckResult{Name: "target " + entry, Detail: dialErr.Error()})
			continue
		}
		_ = conn.Close()
		out = append(out, CheckResult{Name: "target " + entry, OK: true, Detail: "reachable from this host"})
	}
	return out
}
