package ui

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"unicode/utf8"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestNotATerminalStaysSilent(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if Enabled(f, "text", env(nil)) {
		t.Fatal("a file is not a terminal; the banner must stay off")
	}
}

func TestOptOuts(t *testing.T) {
	// Even on a terminal these turn it off; os.Stdin under `go test` is not
	// one, so this only proves the checks run before the terminal test.
	for _, c := range []struct {
		format string
		env    map[string]string
	}{
		{"json", nil},
		{"text", map[string]string{"NO_COLOR": "1"}},
		{"text", map[string]string{"CI": "true"}},
		{"text", map[string]string{"TERM": "dumb"}},
	} {
		if Enabled(os.Stdin, c.format, env(c.env)) {
			t.Errorf("%+v should disable the banner", c)
		}
	}
}

func TestBannerCentersTitleAndLink(t *testing.T) {
	lines := Lines("v1.2.3", 80, false)
	var link, title string
	for _, l := range lines {
		if strings.Contains(l, Link) {
			link = l
		}
		if strings.Contains(l, "DSUI Agent") {
			title = l
		}
	}
	if link == "" || title == "" {
		t.Fatalf("missing title or link:\n%s", strings.Join(lines, "\n"))
	}
	left := len(link) - len(strings.TrimLeft(link, " "))
	right := 80 - utf8.RuneCountInString(link)
	if d := right - left; d < 0 || d > 1 {
		t.Fatalf("link is not centered: left %d, right %d", left, right)
	}
	if !strings.Contains(title, "v1.2.3") {
		t.Fatalf("title lacks the version: %q", title)
	}
}

func TestPlainBannerHasNoEscapes(t *testing.T) {
	if strings.Contains(strings.Join(Lines("v1", 80, false), ""), "\x1b") {
		t.Fatal("uncolored banner must not contain escape codes")
	}
}

func TestGateKeepsOrderAndLosesNothing(t *testing.T) {
	var out bytes.Buffer
	g := NewGate(&out)
	_, _ = g.Write([]byte("first\n"))
	_, _ = g.Write([]byte("second\n"))
	if out.Len() != 0 {
		t.Fatal("a closed gate must hold writes")
	}
	if err := g.Open(); err != nil {
		t.Fatal(err)
	}
	_, _ = g.Write([]byte("third\n"))
	if out.String() != "first\nsecond\nthird\n" {
		t.Fatalf("got %q", out.String())
	}
}
