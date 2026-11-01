package topology

import (
	"fmt"
	"sort"
	"strings"
)

// Diagnostic is one validation finding. Errors block apply; warnings do not.
type Diagnostic struct {
	Severity string `json:"severity"` // error | warning
	File     string `json:"file"`
	Line     int    `json:"line"`
	Col      int    `json:"col,omitempty"`
	Message  string `json:"message"`
	Fix      string `json:"fix,omitempty"`
}

func (d Diagnostic) String() string {
	loc := d.File
	if d.Line > 0 {
		loc = fmt.Sprintf("%s:%d", d.File, d.Line)
		if d.Col > 0 {
			loc = fmt.Sprintf("%s:%d", loc, d.Col)
		}
	}
	s := fmt.Sprintf("%s: %s: %s", loc, d.Severity, d.Message)
	if d.Fix != "" {
		s += "\n    fix: " + d.Fix
	}
	return s
}

// Diagnostics is a list of findings.
type Diagnostics []Diagnostic

func (ds *Diagnostics) errorf(file string, line, col int, fix, format string, a ...any) {
	*ds = append(*ds, Diagnostic{Severity: "error", File: file, Line: line, Col: col, Message: fmt.Sprintf(format, a...), Fix: fix})
}

func (ds *Diagnostics) warnf(file string, line, col int, fix, format string, a ...any) {
	*ds = append(*ds, Diagnostic{Severity: "warning", File: file, Line: line, Col: col, Message: fmt.Sprintf(format, a...), Fix: fix})
}

// HasErrors reports whether any finding is an error.
func (ds Diagnostics) HasErrors() bool {
	for _, d := range ds {
		if d.Severity == "error" {
			return true
		}
	}
	return false
}

// Count returns the number of errors and warnings.
func (ds Diagnostics) Count() (errs, warns int) {
	for _, d := range ds {
		if d.Severity == "error" {
			errs++
		} else {
			warns++
		}
	}
	return
}

// Sort orders findings by file and line.
func (ds Diagnostics) Sort() {
	sort.SliceStable(ds, func(i, j int) bool {
		if ds[i].File != ds[j].File {
			return ds[i].File < ds[j].File
		}
		return ds[i].Line < ds[j].Line
	})
}

// suggest returns the closest candidate to s (Levenshtein), if close enough.
func suggest(s string, candidates []string) string {
	best, bestD := "", 1<<30
	for _, c := range candidates {
		d := levenshtein(strings.ToLower(s), strings.ToLower(c))
		if d < bestD {
			best, bestD = c, d
		}
	}
	limit := len(s)/3 + 1
	if limit < 2 {
		limit = 2
	}
	if bestD <= limit {
		return best
	}
	return ""
}

func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}

// didYouMean formats a fix hint for an unknown reference.
func didYouMean(s string, candidates []string, what string) string {
	if g := suggest(s, candidates); g != "" {
		return fmt.Sprintf("did you mean %q? (%s)", g, what)
	}
	if len(candidates) > 12 {
		return fmt.Sprintf("use an existing %s id; run `vpnmap get %ss` to list them", what, what)
	}
	return fmt.Sprintf("use one of the %s ids: %s", what, strings.Join(candidates, ", "))
}
