package topology

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration is a time.Duration written as "15s", "3m", "14d".
type Duration time.Duration

// ParseDuration accepts Go durations plus a "d" (days) suffix.
func ParseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if strings.HasSuffix(s, "d") {
		n, err := strconv.ParseFloat(strings.TrimSuffix(s, "d"), 64)
		if err != nil {
			return 0, fmt.Errorf("invalid duration %q", s)
		}
		return time.Duration(n * 24 * float64(time.Hour)), nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q (use e.g. 15s, 3m, 2h, 14d)", s)
	}
	return d, nil
}

func (d Duration) D() time.Duration { return time.Duration(d) }

func (d Duration) String() string { return FormatDuration(time.Duration(d)) }

// FormatDuration prints a compact duration ("3m", "1h30m", "14d").
func FormatDuration(d time.Duration) string {
	if d == 0 {
		return "0s"
	}
	if d%(24*time.Hour) == 0 {
		return fmt.Sprintf("%dd", d/(24*time.Hour))
	}
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	v, err := ParseDuration(n.Value)
	if err != nil {
		return err
	}
	*d = Duration(v)
	return nil
}

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).Seconds())
}

// StringList accepts a string or a list of strings.
type StringList []string

func (s *StringList) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.ScalarNode:
		*s = splitList(n.Value)
		return nil
	case yaml.SequenceNode:
		var out []string
		for _, c := range n.Content {
			out = append(out, c.Value)
		}
		*s = out
		return nil
	}
	return fmt.Errorf("expected a string or a list of strings")
}

func splitList(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// CheckList accepts one check or a list of checks.
type CheckList []Check

func (c *CheckList) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.MappingNode:
		var one Check
		if err := one.UnmarshalYAML(n); err != nil {
			return err
		}
		*c = CheckList{one}
		return nil
	case yaml.SequenceNode:
		var out CheckList
		for _, item := range n.Content {
			var one Check
			if err := one.UnmarshalYAML(item); err != nil {
				return err
			}
			out = append(out, one)
		}
		*c = out
		return nil
	}
	return fmt.Errorf("line %d: expected a check or a list of checks", n.Line)
}

// checkReserved are the keys of a check that are not collector parameters.
var checkReserved = map[string]bool{"id": true, "title": true, "collector": true, "on_fail": true, "label": true, "unit": true, "fallback": true}

// UnmarshalYAML reads an inline check: reserved keys go to fields, the rest
// become Params with their YAML types preserved.
func (c *Check) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: a check must be a mapping like { collector: tcp-connect, ... }", n.Line)
	}
	c.Line = n.Line
	c.Params = map[string]any{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i].Value, n.Content[i+1]
		switch k {
		case "id":
			c.ID = v.Value
		case "title":
			c.Title = v.Value
		case "collector":
			c.Collector = v.Value
		case "on_fail":
			c.OnFail = v.Value
		case "label":
			c.Label = v.Value
		case "unit":
			c.Unit = v.Value
		case "fallback":
			var fb Check
			if err := fb.UnmarshalYAML(v); err != nil {
				return err
			}
			c.Fallback = &fb
		default:
			var val any
			if err := v.Decode(&val); err != nil {
				return fmt.Errorf("line %d: %v", v.Line, err)
			}
			c.Params[k] = val
		}
	}
	return nil
}

// Str returns a string parameter (or "").
func (c *Check) Str(name string) string {
	v, ok := c.Params[name]
	if !ok || v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	default:
		return fmt.Sprint(t)
	}
}

// Int returns an integer parameter (or def).
func (c *Check) Int(name string, def int) int {
	switch t := c.Params[name].(type) {
	case int:
		return t
	case int64:
		return int(t)
	case float64:
		return int(t)
	case string:
		if n, err := strconv.Atoi(t); err == nil {
			return n
		}
	}
	return def
}

// Dur returns a duration parameter (or def).
func (c *Check) Dur(name string, def time.Duration) time.Duration {
	s := c.Str(name)
	if s == "" {
		return def
	}
	d, err := ParseDuration(s)
	if err != nil {
		return def
	}
	return d
}

// Map returns a mapping parameter (e.g. an nft match).
func (c *Check) Map(name string) map[string]any {
	if m, ok := c.Params[name].(map[string]any); ok {
		return m
	}
	return nil
}
