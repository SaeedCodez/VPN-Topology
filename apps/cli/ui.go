package main

import (
	"fmt"
	"io"
	"math"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// Terminal output helpers: colours (only on a TTY, never with NO_COLOR),
// status dots, aligned tables and human units.

func colorOn() bool {
	if g.NoColor || os.Getenv("NO_COLOR") != "" || g.JSON {
		return false
	}
	fi, err := os.Stdout.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func paint(code, s string) string {
	if !colorOn() {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

func bold(s string) string  { return paint("1", s) }
func dim(s string) string   { return paint("2", s) }
func red(s string) string   { return paint("31", s) }
func green(s string) string { return paint("32", s) }
func amber(s string) string { return paint("33", s) }
func grey(s string) string  { return paint("90", s) }

// statusText renders a status with a dot, e.g. "● down".
func statusText(s string) string {
	switch s {
	case "ok":
		return green("● ok")
	case "degraded":
		return amber("● degraded")
	case "down":
		return red("● down")
	case "affected":
		return amber("◐ affected")
	default:
		return grey("○ " + orDash(s))
	}
}

func statusWord(s string) string {
	switch s {
	case "ok":
		return green(strings.ToUpper(s))
	case "degraded":
		return amber(strings.ToUpper(s))
	case "down":
		return red(strings.ToUpper(s))
	}
	return grey(strings.ToUpper(orDash(s)))
}

func orDash(s string) string {
	if s == "" {
		return "–"
	}
	return s
}

var ansi = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func visibleLen(s string) int { return utf8.RuneCountInString(ansi.ReplaceAllString(s, "")) }

// table prints rows with aligned columns (ANSI-aware).
type table struct {
	head     []string
	rows     [][]string
	hideHead bool
}

func newTable(head ...string) *table { return &table{head: head} }

func (t *table) add(cells ...string) { t.rows = append(t.rows, cells) }

func (t *table) print(w io.Writer) {
	widths := make([]int, len(t.head))
	for i, h := range t.head {
		widths[i] = visibleLen(h)
	}
	for _, r := range t.rows {
		for i, c := range r {
			if i < len(widths) && visibleLen(c) > widths[i] {
				widths[i] = visibleLen(c)
			}
		}
	}
	line := func(cells []string, style func(string) string) {
		var b strings.Builder
		for i, c := range cells {
			if i >= len(widths) {
				break
			}
			if style != nil {
				c = style(c)
			}
			b.WriteString(c)
			if i < len(cells)-1 {
				b.WriteString(strings.Repeat(" ", widths[i]-visibleLen(c)+2))
			}
		}
		fmt.Fprintln(w, strings.TrimRight(b.String(), " "))
	}
	if !t.hideHead {
		line(t.head, dim)
	}
	for _, r := range t.rows {
		line(r, nil)
	}
}

// bps formats bits per second.
func bps(v *float64) string {
	if v == nil {
		return grey("–")
	}
	x := *v
	switch {
	case x >= 1e9:
		return fmt.Sprintf("%.2f Gbps", x/1e9)
	case x >= 1e6:
		return fmt.Sprintf("%.1f Mbps", x/1e6)
	case x >= 1e3:
		return fmt.Sprintf("%.0f Kbps", x/1e3)
	}
	return fmt.Sprintf("%.0f bps", x)
}

func num(v *float64) string {
	if v == nil {
		return grey("–")
	}
	return thousands(*v)
}

func thousands(x float64) string {
	if math.Abs(x) < 1000 {
		if x == math.Trunc(x) {
			return fmt.Sprintf("%.0f", x)
		}
		return fmt.Sprintf("%.1f", x)
	}
	s := fmt.Sprintf("%.0f", x)
	var out []string
	for len(s) > 3 {
		out = append([]string{s[len(s)-3:]}, out...)
		s = s[:len(s)-3]
	}
	return strings.Join(append([]string{s}, out...), ",")
}

func ago(ts string) string {
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return orDash(ts)
	}
	return humanDur(time.Since(t)) + " ago"
}

func humanDur(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

var tehran = func() *time.Location {
	l, err := time.LoadLocation("Asia/Tehran")
	if err != nil {
		return time.FixedZone("IRST", 3*3600+1800)
	}
	return l
}()

// localTime renders an RFC 3339 time in Tehran time.
func localTime(ts string) string {
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return orDash(ts)
	}
	return t.In(tehran).Format("2006-01-02 15:04:05") + " IRST"
}

func shortTime(ts string) string {
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return orDash(ts)
	}
	lt := t.In(tehran)
	if time.Since(t) < 20*time.Hour {
		return lt.Format("15:04:05")
	}
	return lt.Format("01-02 15:04")
}
