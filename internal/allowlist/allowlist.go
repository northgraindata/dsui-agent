// Package allowlist decides which targets the agent may connect to.
// The list is configured only on the customer side; the DSUI side cannot extend it.
package allowlist

import (
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
)

type rule struct {
	raw      string
	host     string // lower case; may start with "*." for subdomain wildcards
	prefix   netip.Prefix
	isPrefix bool
	port     int // 0 means any port
}

// List is an immutable set of allowed targets.
type List struct {
	rules []rule
}

// Parse builds a list from entries such as:
//
//	airflow.internal:8080     exact host and port
//	db.internal:*             any port on that host
//	*.corp.local:443          any subdomain, one port
//	10.0.0.0/16:8123          CIDR range, one port (matches IP literals only)
func Parse(entries []string) (*List, error) {
	l := &List{}
	for _, e := range entries {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		r, err := parseRule(e)
		if err != nil {
			return nil, err
		}
		l.rules = append(l.rules, r)
	}
	return l, nil
}

func parseRule(entry string) (rule, error) {
	idx := strings.LastIndex(entry, ":")
	if idx <= 0 || idx == len(entry)-1 {
		return rule{}, fmt.Errorf("allowlist entry %q must look like host:port", entry)
	}
	hostPart, portPart := entry[:idx], entry[idx+1:]
	r := rule{raw: entry}
	if portPart != "*" {
		p, err := strconv.Atoi(portPart)
		if err != nil || p < 1 || p > 65535 {
			return rule{}, fmt.Errorf("allowlist entry %q has an invalid port", entry)
		}
		r.port = p
	}
	if strings.Contains(hostPart, "/") {
		prefix, err := netip.ParsePrefix(hostPart)
		if err != nil {
			return rule{}, fmt.Errorf("allowlist entry %q has an invalid CIDR: %w", entry, err)
		}
		r.prefix, r.isPrefix = prefix.Masked(), true
		return r, nil
	}
	hostPart = strings.TrimSuffix(strings.TrimPrefix(strings.ToLower(hostPart), "["), "]")
	if hostPart == "" || strings.ContainsAny(hostPart, " \t") {
		return rule{}, fmt.Errorf("allowlist entry %q has an invalid host", entry)
	}
	if strings.Contains(hostPart, "*") && !strings.HasPrefix(hostPart, "*.") {
		return rule{}, fmt.Errorf("allowlist entry %q: wildcards are only allowed as a leading \"*.\"", entry)
	}
	r.host = hostPart
	return r, nil
}

// Entries returns the configured entries as written.
func (l *List) Entries() []string {
	out := make([]string, len(l.rules))
	for i, r := range l.rules {
		out[i] = r.raw
	}
	return out
}

// Allows reports whether "host:port" may be dialed. It also returns the
// normalized host and port.
func (l *List) Allows(target string) (host string, port int, ok bool) {
	h, p, err := net.SplitHostPort(target)
	if err != nil {
		return "", 0, false
	}
	port, err = strconv.Atoi(p)
	if err != nil || port < 1 || port > 65535 {
		return "", 0, false
	}
	host = strings.ToLower(strings.TrimSuffix(h, "."))
	addr, addrErr := netip.ParseAddr(host)
	for _, r := range l.rules {
		if r.port != 0 && r.port != port {
			continue
		}
		switch {
		case r.isPrefix:
			if addrErr == nil && r.prefix.Contains(addr.Unmap()) {
				return host, port, true
			}
		case strings.HasPrefix(r.host, "*."):
			if addrErr != nil && strings.HasSuffix(host, r.host[1:]) && len(host) > len(r.host)-1 {
				return host, port, true
			}
		case r.host == host:
			return host, port, true
		}
	}
	return host, port, false
}
