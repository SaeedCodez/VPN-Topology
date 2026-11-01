package main

import (
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

func itoa(n int) string { return strconv.Itoa(n) }

// fmtMetric formats a value with its unit like the board does.
func fmtMetric(v *float64, unit string) string {
	if v == nil {
		return grey("–")
	}
	x := *v
	switch unit {
	case "%":
		return fmt.Sprintf("%.0f%%", x)
	case "bps":
		return bps(v)
	case "ms":
		if x < 10 {
			return fmt.Sprintf("%.1f ms", x)
		}
		return fmt.Sprintf("%.0f ms", x)
	case "s":
		return humanDur(time.Duration(x * float64(time.Second)))
	case "days":
		return fmt.Sprintf("%.0f d", x)
	case "users", "conns":
		return thousands(math.Round(x)) // the label already says what is counted
	case "":
		return thousands(x)
	}
	return thousands(x) + " " + unit
}

// usersText renders a user count, "≈" for estimates.
func usersText(n *float64, source string) string {
	if n == nil {
		return grey("–")
	}
	s := thousands(math.Round(*n))
	if source == "estimate" || source == "mixed" {
		return "≈" + s
	}
	return s
}

func impactText(s string) string {
	switch s {
	case "root":
		return red("root cause")
	case "affected":
		return amber("affected")
	}
	return ""
}

// since renders "2026-11-01 13:43 IRST (2h05m ago)".
func sinceText(ts string) string {
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return orDash(ts)
	}
	return t.In(tehran).Format("2006-01-02 15:04") + " IRST, " + humanDur(time.Since(t)) + " ago"
}

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

// exitFromStatus sets the process exit code from an overall status.
func exitFromStatus(s string) {
	switch s {
	case "ok":
		exitCode = ExitOK
	case "down":
		exitCode = ExitDown
	default:
		exitCode = ExitDegraded
	}
}

// printRunbook prints read-only diagnostics, never executed.
func printRunbook(cmds []runbookCmd, indent string) {
	seen := map[string]bool{}
	fmt.Println(indent + bold("Read-only diagnostics (not executed; copy and run them yourself):"))
	for _, r := range cmds {
		if seen[r.Cmd] {
			continue
		}
		seen[r.Cmd] = true
		from := ""
		if r.From != "" {
			from = dim(" (" + r.From + ")")
		}
		fmt.Printf("%s  %s%s\n", indent, dim("# "+scrub(r.Title)), from)
		fmt.Printf("%s  %s\n", indent, scrubCmd(r.Cmd))
		if r.Notes != "" {
			fmt.Printf("%s  %s\n", indent, dim("# "+scrub(r.Notes)))
		}
	}
}

func severityText(s string) string {
	switch s {
	case "critical":
		return red(s)
	case "warning":
		return amber(s)
	case "ok":
		return green(s)
	}
	return grey(s)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

// sparkline draws values (nil = gap) in at most width columns.
func sparkline(vals []*float64, width int) string {
	if len(vals) == 0 {
		return ""
	}
	if width <= 0 {
		width = 60
	}
	// Bucket into width columns (average of the non-null values).
	cols := len(vals)
	if cols > width {
		cols = width
	}
	buckets := make([]*float64, cols)
	for i := 0; i < cols; i++ {
		lo := i * len(vals) / cols
		hi := (i + 1) * len(vals) / cols
		var sum float64
		n := 0
		for _, v := range vals[lo:hi] {
			if v != nil {
				sum += *v
				n++
			}
		}
		if n > 0 {
			avg := sum / float64(n)
			buckets[i] = &avg
		}
	}
	lo, hi := math.Inf(1), math.Inf(-1)
	for _, v := range buckets {
		if v != nil {
			lo = math.Min(lo, *v)
			hi = math.Max(hi, *v)
		}
	}
	ticks := []rune("▁▂▃▄▅▆▇█")
	var b strings.Builder
	for _, v := range buckets {
		switch {
		case v == nil:
			b.WriteRune(' ')
		case hi == lo:
			b.WriteRune(ticks[3])
		default:
			i := int(math.Round((*v - lo) / (hi - lo) * float64(len(ticks)-1)))
			b.WriteRune(ticks[i])
		}
	}
	return b.String()
}

// stats returns min, avg, max and last of the non-null values.
func stats(vals []*float64) (mn, avg, mx, last *float64) {
	var sum float64
	n := 0
	for _, v := range vals {
		if v == nil {
			continue
		}
		x := *v
		if mn == nil || x < *mn {
			mn = &x
		}
		if mx == nil || x > *mx {
			mx = &x
		}
		sum += x
		n++
		last = &x
	}
	if n > 0 {
		a := sum / float64(n)
		avg = &a
	}
	return
}

// isTTY reports whether f is a terminal.
func isTTY(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func headerTime(at string, live bool) string {
	s := localTime(at)
	if !live {
		s += " " + amber("(time travel)")
	}
	return s
}

// line prints s without trailing blanks.
func line(s string) { fmt.Println(strings.TrimRight(s, " ")) }

// usersWord renders "1 user" / "≈1,742 users".
func usersWord(text string, n float64) string {
	if n == 1 {
		return text + " user"
	}
	return text + " users"
}
