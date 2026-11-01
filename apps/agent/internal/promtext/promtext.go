// Package promtext parses the Prometheus text exposition format
// (https://prometheus.io/docs/instrumenting/exposition_formats/), keeping
// only allow-listed metric names. It is intentionally small: no histograms
// as objects, no exemplars; every line is just name{labels} value [ts].
package promtext

import (
	"bufio"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
)

// Sample is one parsed line.
type Sample struct {
	Name   string
	Labels map[string]string
	Value  float64
}

// Parse reads the exposition text from r and returns samples whose metric
// name is in allow (an empty allow-list keeps nothing). At most max samples
// are returned; the rest are dropped and counted in dropped. NaN and ±Inf
// values are skipped because JSON cannot carry them.
func Parse(r io.Reader, allow map[string]bool, max int) (out []Sample, dropped int, err error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == '#' {
			continue
		}
		name := metricName(line)
		if !allow[name] {
			continue
		}
		s, perr := ParseLine(line)
		if perr != nil {
			return out, dropped, fmt.Errorf("line %d: %w", lineNo, perr)
		}
		if math.IsNaN(s.Value) || math.IsInf(s.Value, 0) {
			continue
		}
		if len(out) >= max {
			dropped++
			continue
		}
		out = append(out, s)
	}
	return out, dropped, sc.Err()
}

// metricName returns the leading metric name of a sample line cheaply, so
// non-allow-listed lines are skipped without full parsing.
func metricName(line string) string {
	for i := 0; i < len(line); i++ {
		if c := line[i]; c == '{' || c == ' ' || c == '\t' {
			return line[:i]
		}
	}
	return line
}

// ParseLine parses `name{l1="v1",l2="v2"} value [timestamp]`.
func ParseLine(line string) (Sample, error) {
	var s Sample
	i := 0
	for i < len(line) && isNameChar(line[i], i == 0) {
		i++
	}
	if i == 0 {
		return s, fmt.Errorf("missing metric name")
	}
	s.Name = line[:i]
	if i < len(line) && line[i] == '{' {
		labels, n, err := parseLabels(line[i:])
		if err != nil {
			return s, err
		}
		s.Labels = labels
		i += n
	}
	rest := strings.Fields(line[i:])
	if len(rest) < 1 || len(rest) > 2 {
		return s, fmt.Errorf("expected value [timestamp] after %s", s.Name)
	}
	v, err := parseValue(rest[0])
	if err != nil {
		return s, fmt.Errorf("%s: bad value %q", s.Name, rest[0])
	}
	s.Value = v
	return s, nil
}

func parseValue(s string) (float64, error) {
	switch s {
	case "+Inf", "Inf":
		return math.Inf(1), nil
	case "-Inf":
		return math.Inf(-1), nil
	case "NaN":
		return math.NaN(), nil
	}
	return strconv.ParseFloat(s, 64)
}

// parseLabels parses `{a="x",b="y\"z"}` and returns the labels and the
// number of bytes consumed (including both braces).
func parseLabels(s string) (map[string]string, int, error) {
	labels := map[string]string{}
	i := 1 // past '{'
	for {
		for i < len(s) && (s[i] == ' ' || s[i] == ',') {
			i++
		}
		if i >= len(s) {
			return nil, 0, fmt.Errorf("unterminated label set")
		}
		if s[i] == '}' {
			if len(labels) == 0 {
				labels = nil
			}
			return labels, i + 1, nil
		}
		start := i
		for i < len(s) && isNameChar(s[i], i == start) {
			i++
		}
		if i == start {
			return nil, 0, fmt.Errorf("bad label name at %d", i)
		}
		name := s[start:i]
		for i < len(s) && s[i] == ' ' {
			i++
		}
		if i+1 >= len(s) || s[i] != '=' || s[i+1] != '"' {
			return nil, 0, fmt.Errorf("label %s: expected =\"", name)
		}
		i += 2
		var b strings.Builder
		for {
			if i >= len(s) {
				return nil, 0, fmt.Errorf("label %s: unterminated value", name)
			}
			c := s[i]
			if c == '"' {
				i++
				break
			}
			if c == '\\' && i+1 < len(s) {
				i++
				switch s[i] {
				case 'n':
					b.WriteByte('\n')
				default: // \\ and \"
					b.WriteByte(s[i])
				}
				i++
				continue
			}
			b.WriteByte(c)
			i++
		}
		labels[name] = b.String()
	}
}

func isNameChar(c byte, first bool) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c == '_', c == ':':
		return true
	case c >= '0' && c <= '9':
		return !first
	}
	return false
}
