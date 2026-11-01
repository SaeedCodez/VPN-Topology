package topology

import (
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// structural validation: walks the YAML tree against the S description and
// reports problems with exact line/column and a fix hint.

type walker struct {
	file string
	ds   *Diagnostics
}

func (w *walker) errAt(n *yaml.Node, fix, format string, a ...any) {
	w.ds.errorf(w.file, n.Line, n.Column, fix, format, a...)
}

func scalarType(n *yaml.Node) string {
	switch n.Tag {
	case "!!int":
		return "integer"
	case "!!float":
		return "number"
	case "!!bool":
		return "boolean"
	case "!!null":
		return "null"
	}
	return "string"
}

func kindName(n *yaml.Node) string {
	switch n.Kind {
	case yaml.MappingNode:
		return "a mapping"
	case yaml.SequenceNode:
		return "a list"
	case yaml.ScalarNode:
		return "a " + scalarType(n)
	}
	return "unknown"
}

func (w *walker) walk(n *yaml.Node, s *S, path string) {
	if n.Kind == yaml.AliasNode {
		n = n.Alias
	}
	if s.Check != "" {
		w.checkSpec(n, s.Check, path)
		return
	}
	if len(s.Alt) > 0 {
		for _, a := range s.Alt {
			if matchesKind(n, a) {
				w.walk(n, a, path)
				return
			}
		}
		w.errAt(n, "", "%s: unexpected %s", path, kindName(n))
		return
	}
	switch s.Type {
	case "object":
		if n.Kind != yaml.MappingNode {
			w.errAt(n, "", "%s must be a mapping, got %s", path, kindName(n))
			return
		}
		if len(s.Fields) == 0 {
			return // free-form object
		}
		seen := map[string]bool{}
		var names []string
		for _, f := range s.Fields {
			names = append(names, f.Name)
		}
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			if seen[k.Value] {
				w.errAt(k, "remove one of the two", "%s: key %q appears twice", path, k.Value)
				continue
			}
			seen[k.Value] = true
			var f *F
			for j := range s.Fields {
				if s.Fields[j].Name == k.Value {
					f = &s.Fields[j]
				}
			}
			if f == nil {
				w.errAt(k, didYouMean(k.Value, names, "field"), "%s: unknown field %q", path, k.Value)
				continue
			}
			w.walk(v, f.S, path+"."+k.Value)
		}
		for _, f := range s.Fields {
			if f.Required && !seen[f.Name] {
				w.errAt(n, fmt.Sprintf("add `%s:`", f.Name), "%s: missing required field %q", path, f.Name)
			}
		}
	case "array":
		if n.Kind != yaml.SequenceNode {
			w.errAt(n, "", "%s must be a list, got %s", path, kindName(n))
			return
		}
		for i, c := range n.Content {
			w.walk(c, s.Items, fmt.Sprintf("%s[%d]", path, i))
		}
	case "string", "integer", "number", "boolean":
		if n.Kind != yaml.ScalarNode {
			w.errAt(n, "", "%s must be a %s, got %s", path, s.Type, kindName(n))
			return
		}
		st := scalarType(n)
		switch s.Type {
		case "integer":
			if st != "integer" {
				w.errAt(n, "", "%s must be an integer, got %q", path, n.Value)
				return
			}
		case "number":
			if st != "integer" && st != "number" {
				w.errAt(n, "", "%s must be a number, got %q", path, n.Value)
				return
			}
		case "boolean":
			if st != "boolean" {
				w.errAt(n, "use true or false", "%s must be true or false, got %q", path, n.Value)
				return
			}
		case "string":
			if st == "null" {
				w.errAt(n, "", "%s is empty", path)
				return
			}
		}
		if len(s.Enum) > 0 && !contains(s.Enum, n.Value) {
			w.errAt(n, didYouMean(n.Value, s.Enum, "value"), "%s: %q is not allowed (allowed: %s)", path, n.Value, strings.Join(s.Enum, ", "))
		}
		if s.Pattern != "" && !regexp.MustCompile(s.Pattern).MatchString(n.Value) {
			w.errAt(n, patternHint(s), "%s: %q has the wrong format", path, n.Value)
		}
	}
}

func patternHint(s *S) string {
	switch s.Pattern {
	case idPattern:
		return "ids use lowercase letters, digits and dashes, e.g. relay-2"
	case sDuration.Pattern:
		return "write a duration like 15s, 3m, 2h or 14d"
	}
	if s.Desc != "" {
		return s.Desc
	}
	return "must match " + s.Pattern
}

func matchesKind(n *yaml.Node, s *S) bool {
	if s.Check != "" {
		return n.Kind == yaml.MappingNode
	}
	switch s.Type {
	case "object":
		return n.Kind == yaml.MappingNode
	case "array":
		return n.Kind == yaml.SequenceNode
	default:
		return n.Kind == yaml.ScalarNode
	}
}

// checkSpec validates an inline collector spec against the catalog.
func (w *walker) checkSpec(n *yaml.Node, usage, path string) {
	if n.Kind != yaml.MappingNode {
		w.errAt(n, "write it as { collector: <name>, ... }", "%s must be a collector spec, got %s", path, kindName(n))
		return
	}
	var coll *yaml.Node
	keys := map[string]*yaml.Node{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		keys[n.Content[i].Value] = n.Content[i+1]
		if n.Content[i].Value == "collector" {
			coll = n.Content[i+1]
		}
	}
	allowed := []string{}
	for _, c := range Catalog {
		if c.UsableAs(usage) {
			allowed = append(allowed, c.Name)
		}
	}
	if coll == nil {
		w.errAt(n, "add `collector: <name>`; run `vpnmap explain collector` for the list", "%s: missing collector", path)
		return
	}
	info := Collector(coll.Value)
	if info == nil {
		w.errAt(coll, didYouMean(coll.Value, CollectorNames(), "collector"), "%s: unknown collector %q", path, coll.Value)
		return
	}
	if !info.UsableAs(usage) {
		w.errAt(coll, "collectors allowed here: "+strings.Join(allowed, ", "), "%s: collector %q cannot be used as %s", path, info.Name, usage)
		return
	}
	base := checkBase(usage)
	var known []string
	for _, f := range base {
		known = append(known, f.Name)
	}
	for _, p := range info.Params {
		known = append(known, p.Name)
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if k.Value == "collector" {
			continue
		}
		var bf *F
		for j := range base {
			if base[j].Name == k.Value {
				bf = &base[j]
			}
		}
		if bf != nil {
			w.walk(v, bf.S, path+"."+k.Value)
			continue
		}
		p := info.Param(k.Value)
		if p == nil {
			w.errAt(k, didYouMean(k.Value, known, "parameter")+fmt.Sprintf("; see `vpnmap explain collector %s`", info.Name),
				"%s: collector %s has no parameter %q", path, info.Name, k.Value)
			continue
		}
		w.walk(v, ParamSchema(*p), path+"."+k.Value)
	}
	for _, p := range info.Params {
		if p.Required && keys[p.Name] == nil {
			w.errAt(n, fmt.Sprintf("add `%s:` (%s)", p.Name, p.Doc), "%s: collector %s needs parameter %q", path, info.Name, p.Name)
		}
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
