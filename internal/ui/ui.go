// Package ui draws the start screen in an interactive terminal. It never
// replaces logs: in containers, services and CI it stays silent and the logs
// are exactly what they would be without it.
package ui

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/term"
)

// Link is shown under the banner until the agent has its own docs.
const Link = "dsui.northgraindata.com"

const (
	reset = "\x1b[0m"
	bold  = "\x1b[1m"
	dim   = "\x1b[2m"
	green = "\x1b[38;5;71m"

	frameDelay = 55 * time.Millisecond
)

// The range is pixel art: every terminal cell holds two square pixels, the
// upper one drawn with the foreground color of '▀' and the lower one with its
// background. Terminals draw half blocks edge to edge, so there are no gaps.

// A peak of the range: where it stands and how high, in pixels.
type peak struct{ x, height int }

var peaks = []peak{
	{5, 7}, {13, 11}, {22, 16}, {31, 12}, {38, 13}, {45, 6},
}

const (
	rangeWidth  = 50
	rangePixels = 16 // pixel rows, two per line
	rangeHeight = rangePixels / 2
)

// Colors of the 256-color palette.
const (
	none      = -1
	snowColor = 230
	lightLeaf = 71
	midLeaf   = 29
	darkLeaf  = 22
)

// heights gives the height of the range at every column, in pixels.
func heights() []int {
	h := make([]int, rangeWidth)
	for x := range h {
		for _, p := range peaks {
			d := x - p.x
			if d < 0 {
				d = -d
			}
			if v := p.height - d; v > h[x] {
				h[x] = v
			}
		}
	}
	return h
}

// pixel is the color at column x, pixel row y counted from the top. The
// shade follows the slope, not the altitude: snow on the high summits, a light
// ridge line along every crest, and darker rock deeper inside the range.
func pixel(h []int, x, y int) int {
	level := rangePixels - y // 1 is the bottom pixel row
	if level > h[x] {
		return none
	}
	depth := h[x] - level // 0 on the surface
	switch {
	case h[x] >= 11 && depth <= 2:
		return snowColor
	case depth == 0:
		return lightLeaf
	case depth <= 4:
		return midLeaf
	default:
		return darkLeaf
	}
}

// rangeLines renders the range, painted or as plain block characters.
func rangeLines(color bool) []string {
	h := heights()
	lines := make([]string, rangeHeight)
	for r := range lines {
		var b strings.Builder
		for x := 0; x < rangeWidth; x++ {
			top, bottom := pixel(h, x, 2*r), pixel(h, x, 2*r+1)
			switch {
			case top == none && bottom == none:
				b.WriteByte(' ')
			case !color && top != none && bottom != none:
				b.WriteString("█")
			case !color && top != none:
				b.WriteString("▀")
			case !color:
				b.WriteString("▄")
			case top == none:
				fmt.Fprintf(&b, "\x1b[38;5;%dm▄%s", bottom, reset)
			case bottom == none:
				fmt.Fprintf(&b, "\x1b[38;5;%dm▀%s", top, reset)
			default:
				fmt.Fprintf(&b, "\x1b[38;5;%d;48;5;%dm▀%s", top, bottom, reset)
			}
		}
		lines[r] = strings.TrimRight(b.String(), " ")
	}
	return lines
}

// Enabled reports whether a start screen belongs on f: only for a person at
// an interactive terminal, never in logs, services, containers without a TTY
// or CI, and never when NO_COLOR or JSON logs are asked for.
func Enabled(f *os.File, logFormat string, getenv func(string) string) bool {
	if logFormat == "json" || getenv("NO_COLOR") != "" || getenv("CI") != "" || getenv("TERM") == "dumb" {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}

// Width is the terminal width, or a sensible default when it is unknown.
func Width(f *os.File) int {
	if w, _, err := term.GetSize(int(f.Fd())); err == nil && w >= 40 {
		return w
	}
	return 80
}

func center(s string, width int) string {
	n := utf8.RuneCountInString(s)
	if n >= width {
		return s
	}
	return strings.Repeat(" ", (width-n)/2) + s
}

// Lines returns the banner, centered for width. With color it is painted.
func Lines(version string, width int, color bool) []string {
	rows := rangeLines(color)
	out := make([]string, 0, len(rows)+5)
	pad := ""
	if width > rangeWidth {
		pad = strings.Repeat(" ", (width-rangeWidth)/2)
	}
	for _, row := range rows {
		out = append(out, pad+row)
	}
	title := "DSUI Agent  " + version
	link := Link
	if color {
		out = append(out, "", center(title, width), center(link, width), "")
		out[len(out)-3] = strings.Replace(out[len(out)-3], title, bold+"DSUI Agent"+reset+"  "+dim+version+reset, 1)
		out[len(out)-2] = strings.Replace(out[len(out)-2], link, green+link+reset, 1)
		return out
	}
	return append(out, "", center(title, width), center(link, width), "")
}

// Show draws the banner on w. With animate, the range rises line by line.
func Show(w io.Writer, version string, width int, animate bool) {
	lines := Lines(version, width, true)
	_, _ = fmt.Fprintln(w)
	for i, line := range lines {
		_, _ = fmt.Fprintln(w, line)
		if animate && i < rangeHeight {
			time.Sleep(frameDelay)
		}
	}
}

// Gate holds what is written to it until Open, then passes everything
// through. Logs written while the banner animates are kept, in order, and
// appear right after it: nothing is lost and nothing interleaves.
type Gate struct {
	mu   sync.Mutex
	out  io.Writer
	buf  bytes.Buffer
	open bool
}

// NewGate returns a closed gate in front of out.
func NewGate(out io.Writer) *Gate { return &Gate{out: out} }

// Write buffers until the gate opens.
func (g *Gate) Write(p []byte) (int, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.open {
		return g.out.Write(p)
	}
	return g.buf.Write(p)
}

// Open flushes what was held and lets later writes through.
func (g *Gate) Open() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.open {
		return nil
	}
	g.open = true
	_, err := g.out.Write(g.buf.Bytes())
	g.buf.Reset()
	return err
}
