package topology

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"
)

// BundleDiff lists what changes when the new bundle (the local files)
// replaces the old one (the topology active on the hub). `vpnmap plan`
// prints it. Line numbers are ignored: they move when unrelated lines change.
type BundleDiff struct {
	OldHash string   `json:"old_hash"`
	NewHash string   `json:"new_hash"`
	Changes []Change `json:"changes"`
}

// Change is one added, removed or changed element.
type Change struct {
	Op    string `json:"op"`   // add | remove | change
	Kind  string `json:"kind"` // map | settings | group | source | component | node | link | route | check | alert
	ID    string `json:"id"`
	Title string `json:"title,omitempty"`
	// File and Line say where the element is defined (the old place for removals).
	File string `json:"file,omitempty"`
	Line int    `json:"line,omitempty"`
	// Owner and Collector are set for checks ("node:panel", "http-probe").
	Owner     string `json:"owner,omitempty"`
	Collector string `json:"collector,omitempty"`
	// Checks lists the checks of an added or removed node/link/route.
	Checks []string      `json:"checks,omitempty"`
	Fields []FieldChange `json:"fields,omitempty"`
}

// FieldChange is one field-level difference (Old is nil for a new field,
// New is nil for a removed one).
type FieldChange struct {
	Path string `json:"path"`
	Old  any    `json:"old"`
	New  any    `json:"new"`
}

// Empty reports whether nothing changes.
func (d *BundleDiff) Empty() bool { return len(d.Changes) == 0 }

// Count returns the number of changes with op (add, remove, change).
func (d *BundleDiff) Count(op string) int {
	n := 0
	for _, c := range d.Changes {
		if c.Op == op {
			n++
		}
	}
	return n
}

// Diff compares two bundles (old = live on the hub, new = local files).
func Diff(old, new *Bundle) (*BundleDiff, error) {
	oj, err := json.Marshal(old)
	if err != nil {
		return nil, err
	}
	nj, err := json.Marshal(new)
	if err != nil {
		return nil, err
	}
	return DiffJSON(oj, nj)
}

// DiffJSON compares two bundles in their JSON form (as `vpnmap bundle`
// prints them and as GET /api/topology returns them).
func DiffJSON(oldRaw, newRaw []byte) (*BundleDiff, error) {
	var o, n map[string]any
	if err := json.Unmarshal(oldRaw, &o); err != nil {
		return nil, fmt.Errorf("old bundle: %w", err)
	}
	if err := json.Unmarshal(newRaw, &n); err != nil {
		return nil, fmt.Errorf("new bundle: %w", err)
	}
	d := &BundleDiff{OldHash: jstr(o["hash"]), NewHash: jstr(n["hash"])}

	om, nm := jobj(o["map"]), jobj(n["map"])
	d.fieldChange("map", "map", "topology/map.yaml", 0, without(om, "settings", "groups", "sources"), without(nm, "settings", "groups", "sources"))
	d.fieldChange("settings", "settings", "topology/map.yaml", 0, jobj(om["settings"]), jobj(nm["settings"]))
	d.keyed("group", "id", jlist(om["groups"]), jlist(nm["groups"]), nil)
	d.keyed("source", "id", jlist(om["sources"]), jlist(nm["sources"]), nil)
	d.keyed("component", "name", jlist(o["components"]), jlist(n["components"]), nil)

	oldChecks, newChecks := map[string]map[string]any{}, map[string]map[string]any{}
	skipOwners := map[string]bool{}
	ownerFile := map[string]string{}
	for _, kind := range []string{"node", "link", "route"} {
		ol, nl := jlist(o[kind+"s"]), jlist(n[kind+"s"])
		for _, e := range ol {
			collectChecks(kind, e, oldChecks)
		}
		for _, e := range append(append([]any{}, ol...), nl...) {
			if x := jobj(e); x != nil {
				ownerFile[kind+":"+jstr(x["id"])], _ = definedAt(x)
			}
		}
		for _, e := range nl {
			collectChecks(kind, e, newChecks)
		}
		d.keyed(kind, "id", ol, nl, func(c *Change, e map[string]any) {
			skipOwners[kind+":"+c.ID] = true
			sub := map[string]map[string]any{}
			collectChecks(kind, e, sub)
			for _, k := range sortedKeys(sub) {
				c.Checks = append(c.Checks, fmt.Sprintf("%s (%s)", k, jstr(sub[k]["collector"])))
			}
		})
	}
	d.checks(oldChecks, newChecks, skipOwners, ownerFile)
	d.keyed("alert", "id", jlist(o["alerts"]), jlist(n["alerts"]), nil)
	return d, nil
}

var kindOrder = map[string]int{"map": 0, "settings": 1, "group": 2, "source": 3, "component": 4, "node": 5, "link": 6, "route": 7, "check": 8, "alert": 9}

// derived fields are computed by the loader from other fields.
var derived = map[string][]string{
	"node":  {"checks", "metrics"},
	"link":  {"traffic", "conns", "health"},
	"route": {"probe", "users", "nodes"},
	"alert": {"expr"},
	"check": {"key", "fallback"},
}

// keyed diffs two lists of objects by their id field.
func (d *BundleDiff) keyed(kind, idField string, ol, nl []any, onAddRemove func(*Change, map[string]any)) {
	index := func(l []any) map[string]map[string]any {
		m := map[string]map[string]any{}
		for _, e := range l {
			if x := jobj(e); x != nil {
				m[jstr(x[idField])] = x
			}
		}
		return m
	}
	om, nm := index(ol), index(nl)
	ids := map[string]bool{}
	for k := range om {
		ids[k] = true
	}
	for k := range nm {
		ids[k] = true
	}
	for _, id := range sortedKeys(ids) {
		oe, ne := om[id], nm[id]
		switch {
		case oe == nil:
			c := newChange("add", kind, id, ne)
			if onAddRemove != nil {
				onAddRemove(&c, ne)
			}
			d.Changes = append(d.Changes, c)
		case ne == nil:
			c := newChange("remove", kind, id, oe)
			if onAddRemove != nil {
				onAddRemove(&c, oe)
			}
			d.Changes = append(d.Changes, c)
		default:
			file, line := definedAt(ne)
			d.fieldChange(kind, id, file, line, without(oe, derived[kind]...), without(ne, derived[kind]...))
		}
	}
}

func (d *BundleDiff) checks(om, nm map[string]map[string]any, skipOwners map[string]bool, ownerFile map[string]string) {
	keys := map[string]bool{}
	for k := range om {
		keys[k] = true
	}
	for k := range nm {
		keys[k] = true
	}
	for _, k := range sortedKeys(keys) {
		owner := k
		if i := strings.LastIndex(k, ":"); i > 0 {
			owner = k[:i]
		}
		if skipOwners[owner] {
			continue
		}
		oe, ne := om[k], nm[k]
		var c Change
		switch {
		case oe == nil:
			c = Change{Op: "add", Kind: "check", ID: k, Line: jnum(ne["line"])}
		case ne == nil:
			c = Change{Op: "remove", Kind: "check", ID: k, Line: jnum(oe["line"])}
		default:
			fields := diffFields("", stripLineKeys(without(oe, derived["check"]...)), stripLineKeys(without(ne, derived["check"]...)))
			if len(fields) == 0 {
				continue
			}
			c = Change{Op: "change", Kind: "check", ID: k, Line: jnum(ne["line"]), Fields: fields}
		}
		e := ne
		if e == nil {
			e = oe
		}
		c.Owner, c.Collector, c.Title, c.File = owner, jstr(e["collector"]), jstr(e["title"]), ownerFile[owner]
		d.Changes = append(d.Changes, c)
	}
}

func (d *BundleDiff) fieldChange(kind, id, file string, line int, oe, ne map[string]any) {
	fields := diffFields("", stripLineKeys(oe), stripLineKeys(ne))
	if len(fields) == 0 {
		return
	}
	d.Changes = append(d.Changes, Change{Op: "change", Kind: kind, ID: id, Title: entityTitle(kind, ne), File: file, Line: line, Fields: fields})
}

func newChange(op, kind, id string, e map[string]any) Change {
	file, line := definedAt(e)
	if kind == "component" {
		file = jstr(e["file"])
	}
	return Change{Op: op, Kind: kind, ID: id, Title: entityTitle(kind, e), File: file, Line: line}
}

// Sort orders the changes by kind, then id (Diff output is already sorted).
func (d *BundleDiff) Sort() {
	sort.SliceStable(d.Changes, func(i, j int) bool {
		a, b := d.Changes[i], d.Changes[j]
		if kindOrder[a.Kind] != kindOrder[b.Kind] {
			return kindOrder[a.Kind] < kindOrder[b.Kind]
		}
		return a.ID < b.ID
	})
}

// collectChecks extracts every check of a node/link/route keyed by its key.
func collectChecks(kind string, e any, out map[string]map[string]any) {
	x := jobj(e)
	if x == nil {
		return
	}
	owner := kind + ":" + jstr(x["id"])
	add := func(v any) {
		c := jobj(v)
		if c == nil {
			return
		}
		key := jstr(c["key"])
		if key == "" {
			key = owner + ":" + jstr(c["id"])
		}
		out[key] = c
		if fb := jobj(c["fallback"]); fb != nil {
			fk := jstr(fb["key"])
			if fk == "" {
				fk = owner + ":" + jstr(fb["id"])
			}
			out[fk] = fb
		}
	}
	for _, f := range derived[kind] {
		switch v := x[f].(type) {
		case []any:
			for _, c := range v {
				add(c)
			}
		case map[string]any:
			add(v)
		}
	}
}

// diffFields flattens both values and lists the paths that differ. Lists of
// scalars (addresses, hops, squads) compare as one value.
func diffFields(prefix string, o, n any) []FieldChange {
	if reflect.DeepEqual(o, n) {
		return nil
	}
	om, oIsMap := o.(map[string]any)
	nm, nIsMap := n.(map[string]any)
	if oIsMap && nIsMap {
		keys := map[string]bool{}
		for k := range om {
			keys[k] = true
		}
		for k := range nm {
			keys[k] = true
		}
		var out []FieldChange
		for _, k := range sortedKeys(keys) {
			ov, oOK := om[k]
			nv, nOK := nm[k]
			if isZero(ov) && isZero(nv) {
				continue // absent, null, "" and [] mean the same
			}
			if !oOK {
				ov = nil
			}
			if !nOK {
				nv = nil
			}
			out = append(out, diffFields(joinPath(prefix, k), ov, nv)...)
		}
		return out
	}
	ol, oIsList := o.([]any)
	nl, nIsList := n.([]any)
	if oIsList && nIsList && (hasObjects(ol) || hasObjects(nl)) {
		var out []FieldChange
		for i := 0; i < len(ol) || i < len(nl); i++ {
			var ov, nv any
			if i < len(ol) {
				ov = ol[i]
			}
			if i < len(nl) {
				nv = nl[i]
			}
			out = append(out, diffFields(fmt.Sprintf("%s[%d]", prefix, i), ov, nv)...)
		}
		return out
	}
	if isZero(o) && isZero(n) {
		return nil
	}
	return []FieldChange{{Path: prefix, Old: o, New: n}}
}

func hasObjects(l []any) bool {
	for _, e := range l {
		switch e.(type) {
		case map[string]any, []any:
			return true
		}
	}
	return false
}

func isZero(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return t == ""
	case []any:
		return len(t) == 0
	case map[string]any:
		return len(t) == 0
	}
	return false
}

func joinPath(prefix, k string) string {
	if prefix == "" {
		return k
	}
	return prefix + "." + k
}

// stripLineKeys returns a deep copy without "line" keys (and so without
// defined_in.line).
func stripLineKeys(v map[string]any) map[string]any {
	out, _ := stripLinesAny(v).(map[string]any)
	return out
}

func stripLinesAny(v any) any {
	switch t := v.(type) {
	case map[string]any:
		m := make(map[string]any, len(t))
		for k, x := range t {
			if k == "line" {
				continue
			}
			m[k] = stripLinesAny(x)
		}
		return m
	case []any:
		l := make([]any, len(t))
		for i, x := range t {
			l[i] = stripLinesAny(x)
		}
		return l
	}
	return v
}

func without(m map[string]any, keys ...string) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	for _, k := range keys {
		delete(out, k)
	}
	return out
}

func definedAt(e map[string]any) (string, int) {
	d := jobj(e["defined_in"])
	if d == nil {
		return "", 0
	}
	return jstr(d["file"]), jnum(d["line"])
}

func entityTitle(kind string, e map[string]any) string {
	keys := []string{"name", "title", "label"}
	if kind == "component" { // its name is the id
		keys = []string{"title"}
	}
	for _, k := range keys {
		if s := jstr(e[k]); s != "" {
			return s
		}
	}
	return ""
}

func jobj(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func jlist(v any) []any {
	l, _ := v.([]any)
	return l
}

func jstr(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

func jnum(v any) int {
	if f, ok := v.(float64); ok {
		return int(f)
	}
	return 0
}

// FormatValue renders a field value compactly for humans ("–" when absent).
func FormatValue(v any) string {
	if v == nil {
		return "–"
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return fmt.Sprint(v)
	}
	return strings.TrimSpace(buf.String())
}

// ParseBundle decodes a bundle from its JSON form (GET /api/topology).
func ParseBundle(raw []byte) (*Bundle, error) {
	var b Bundle
	if err := json.Unmarshal(raw, &b); err != nil {
		return nil, err
	}
	return &b, nil
}

// UnmarshalJSON reads a duration written by MarshalJSON (seconds) or as
// text ("30s"), so bundles round-trip through JSON.
func (d *Duration) UnmarshalJSON(raw []byte) error {
	var secs float64
	if err := json.Unmarshal(raw, &secs); err == nil {
		*d = Duration(secs * float64(time.Second))
		return nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return fmt.Errorf("duration must be seconds or a string like 30s")
	}
	v, err := ParseDuration(s)
	if err != nil {
		return err
	}
	*d = Duration(v)
	return nil
}
