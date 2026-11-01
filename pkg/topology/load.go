package topology

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Options controls loading.
type Options struct {
	// Root is the repository root (the directory that contains topology/).
	Root string
}

// TopologyDir returns <root>/topology.
func (o Options) TopologyDir() string { return filepath.Join(o.Root, "topology") }

// FindRoot walks up from dir to the directory that contains topology/map.yaml.
func FindRoot(dir string) (string, error) {
	d, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(d, "topology", "map.yaml")); err == nil {
			return d, nil
		}
		parent := filepath.Dir(d)
		if parent == d {
			return "", fmt.Errorf("no topology/map.yaml found in %s or any parent directory", dir)
		}
		d = parent
	}
}

// Load reads every topology file under opts.Root, validates it and returns
// the resolved bundle. The bundle is nil only when the files cannot be read
// or parsed at all; with semantic errors a partial bundle is still returned.
func Load(opts Options) (*Bundle, Diagnostics) {
	var ds Diagnostics
	b := &Bundle{Schema: SchemaVersion, Layout: map[string]Pos{}, Catalog: Catalog}

	mapPath := filepath.Join(opts.TopologyDir(), "map.yaml")
	rel := relPath(opts.Root, mapPath)
	mapNode, err := parseFile(mapPath)
	if err != nil {
		ds.errorf(rel, lineOf(err), 0, "fix the YAML syntax", "%v", cleanYAMLErr(err))
		return nil, ds
	}
	(&walker{file: rel, ds: &ds}).walk(mapNode, MapSchema, "map")
	if err := mapNode.Decode(&b.Map); err != nil {
		ds.errorf(rel, lineOf(err), 0, "", "%v", cleanYAMLErr(err))
	}

	files, _ := filepath.Glob(filepath.Join(opts.TopologyDir(), "components", "*.yaml"))
	more, _ := filepath.Glob(filepath.Join(opts.TopologyDir(), "components", "*.yml"))
	files = append(files, more...)
	sort.Strings(files)
	if len(files) == 0 {
		ds.errorf(relPath(opts.Root, filepath.Join(opts.TopologyDir(), "components")), 0, 0,
			"create one with `vpnmap new component <name>`", "no component files found")
	}
	for _, f := range files {
		rel := relPath(opts.Root, f)
		scanSecrets(f, rel, &ds)
		n, err := parseFile(f)
		if err != nil {
			ds.errorf(rel, lineOf(err), 0, "fix the YAML syntax", "%v", cleanYAMLErr(err))
			continue
		}
		before := len(ds)
		(&walker{file: rel, ds: &ds}).walk(n, ComponentSchema, "component")
		var c Component
		if err := n.Decode(&c); err != nil {
			if len(ds) == before {
				ds.errorf(rel, lineOf(err), 0, "", "%v", cleanYAMLErr(err))
			}
			continue
		}
		c.File = rel
		setLines(n, &c, rel)
		b.Components = append(b.Components, c)
	}

	layoutPath := filepath.Join(opts.TopologyDir(), "layout.yaml")
	if raw, err := os.ReadFile(layoutPath); err == nil {
		var lf struct {
			Positions map[string]Pos `yaml:"positions"`
		}
		if err := yaml.Unmarshal(raw, &lf); err != nil {
			ds.warnf(relPath(opts.Root, layoutPath), lineOf(err), 0, "regenerate it with `vpnmap layout pull`", "layout.yaml: %v", cleanYAMLErr(err))
		} else if lf.Positions != nil {
			b.Layout = lf.Positions
		}
	}

	resolve(b, &ds)
	ds.Sort()
	b.Hash = b.ContentHash()
	return b, ds
}

func parseFile(path string) (*yaml.Node, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return nil, fmt.Errorf("file is empty")
	}
	return doc.Content[0], nil
}

// setLines records the line of every node, link, route and alert.
func setLines(root *yaml.Node, c *Component, file string) {
	section := func(name string) []*yaml.Node {
		for i := 0; i+1 < len(root.Content); i += 2 {
			if root.Content[i].Value == name && root.Content[i+1].Kind == yaml.SequenceNode {
				return root.Content[i+1].Content
			}
		}
		return nil
	}
	def := func(n *yaml.Node) DefinedIn {
		d := DefinedIn{Component: c.Component, File: file, Line: n.Line, fields: map[string]int{}}
		for i := 0; i+1 < len(n.Content); i += 2 {
			d.fields[n.Content[i].Value] = n.Content[i+1].Line
		}
		return d
	}
	for i, n := range section("nodes") {
		if i < len(c.Nodes) {
			c.Nodes[i].Defined = def(n)
		}
	}
	for i, n := range section("links") {
		if i < len(c.Links) {
			c.Links[i].Defined = def(n)
		}
	}
	for i, n := range section("routes") {
		if i < len(c.Routes) {
			c.Routes[i].Defined = def(n)
		}
	}
	for i, n := range section("alerts") {
		if i < len(c.Alerts) {
			c.Alerts[i].Defined = def(n)
		}
	}
}

func relPath(root, p string) string {
	r, err := filepath.Rel(root, p)
	if err != nil {
		return p
	}
	return filepath.ToSlash(r)
}

func lineOf(err error) int {
	var line int
	if _, e := fmt.Sscanf(afterPrefix(err.Error(), "line "), "%d", &line); e == nil {
		return line
	}
	return 0
}

func afterPrefix(s, p string) string {
	if i := strings.Index(s, p); i >= 0 {
		return s[i+len(p):]
	}
	return ""
}

func cleanYAMLErr(err error) string {
	return strings.TrimPrefix(err.Error(), "yaml: ")
}

// ContentHash hashes the topology content, excluding source info and layout.
func (b *Bundle) ContentHash() string {
	cp := *b
	cp.Source = BundleSource{}
	cp.Layout = nil
	cp.Hash = ""
	cp.Catalog = nil
	raw, _ := json.Marshal(cp)
	// Line numbers move when unrelated lines change; they are not content.
	raw = stripLines(raw)
	s := sha256.Sum256(raw)
	return hex.EncodeToString(s[:])[:16]
}

func stripLines(raw []byte) []byte {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return raw
	}
	var walk func(any) any
	walk = func(x any) any {
		switch t := x.(type) {
		case map[string]any:
			delete(t, "line")
			if d, ok := t["defined_in"].(map[string]any); ok {
				delete(d, "line")
			}
			for k, v := range t {
				t[k] = walk(v)
			}
		case []any:
			for i := range t {
				t[i] = walk(t[i])
			}
		}
		return x
	}
	out, _ := json.Marshal(walk(v))
	return out
}

// JSON renders the bundle (stable, indented).
func (b *Bundle) JSON() []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	_ = enc.Encode(b)
	return buf.Bytes()
}

// Stamp fills in Source with git information.
func (b *Bundle) Stamp(commit, branch string, dirty bool, generator string) {
	b.Source = BundleSource{GitCommit: commit, GitBranch: branch, Dirty: dirty, GeneratedAt: time.Now().UTC().Format(time.RFC3339), Generator: generator}
}

// NodeByID returns the node with id, or nil.
func (b *Bundle) NodeByID(id string) *Node {
	for i := range b.Nodes {
		if b.Nodes[i].ID == id {
			return &b.Nodes[i]
		}
	}
	return nil
}

// LinkByID returns the link with id, or nil.
func (b *Bundle) LinkByID(id string) *Link {
	for i := range b.Links {
		if b.Links[i].ID == id {
			return &b.Links[i]
		}
	}
	return nil
}

// RouteByID returns the route with id, or nil.
func (b *Bundle) RouteByID(id string) *Route {
	for i := range b.Routes {
		if b.Routes[i].ID == id {
			return &b.Routes[i]
		}
	}
	return nil
}
