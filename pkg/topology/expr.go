package topology

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

// Alert conditions are small expressions such as
//
//	link.wg-game.health == down for 2m
//	route.main.probe_failures >= 3
//	link.e3.health == down and node.xbridger-node-3.check.xbridger-api.status == ok
//	node.*.rebooted == true
//	collector.tls-expiry.status != ok
//
// They are parsed here once and shipped to the hub as an AST, so the hub has
// no parser of its own.

// Expr is a node of the alert AST.
type Expr struct {
	Op    string  `json:"op"`              // and | or | not | cmp
	Args  []*Expr `json:"args,omitempty"`  // and/or/not
	Ref   *Ref    `json:"ref,omitempty"`   // cmp
	Cmp   string  `json:"cmp,omitempty"`   // == != > >= < <=
	Value *Value  `json:"value,omitempty"` // cmp
	// For is only set on the root: the condition must hold for this long
	// (Seconds) or for this many consecutive evaluations (Samples).
	For *For `json:"for,omitempty"`
}

// Ref points at a field of an entity: node.<id>.<field>, link.<id>.<field>,
// route.<id>.<field>, <kind>.<id>.check.<check id>.<field>, collector.<name>.<field>.
// ID may be "*" (the alert fires once per matching entity).
type Ref struct {
	Kind  string `json:"kind"` // node | link | route | collector
	ID    string `json:"id"`
	Check string `json:"check,omitempty"`
	Field string `json:"field"`
	Text  string `json:"text"`
}

// Value is a literal: a status word, a bool or a number (normalised to base
// units: seconds, bits per second, percent).
type Value struct {
	Type   string  `json:"type"` // status | bool | number
	Status string  `json:"status,omitempty"`
	Bool   bool    `json:"bool,omitempty"`
	Number float64 `json:"number,omitempty"`
	Text   string  `json:"text"`
}

// For is the hold condition.
type For struct {
	Seconds float64 `json:"seconds,omitempty"`
	Samples int     `json:"samples,omitempty"`
}

// Field types per entity kind.
var refFields = map[string]map[string]string{
	"node": {
		"status": "status", "health": "status", "rebooted": "bool", "uptime": "number",
		"cpu": "number", "mem": "number", "disk": "number", "load": "number", "agent_age": "number",
	},
	"link": {
		"status": "status", "health": "status", "up_bps": "number", "down_bps": "number", "total_bps": "number",
		"conns": "number", "traffic_drop": "number",
	},
	"route": {
		"status": "status", "probe": "status", "probe_failures": "number", "users": "number", "latency": "number",
	},
	"check":     {"status": "status", "value": "number", "latency": "number"},
	"collector": {"status": "status", "value": "number"},
}

// RefFields returns the valid fields of an entity kind (for error messages).
func RefFields(kind string) []string {
	var out []string
	for f := range refFields[kind] {
		out = append(out, f)
	}
	sortStrings(out)
	return out
}

// FieldType returns the type of a ref's field ("" if unknown).
func (r *Ref) FieldType() string {
	if r.Check != "" {
		return refFields["check"][r.Field]
	}
	return refFields[r.Kind][r.Field]
}

type tok struct {
	s   string
	pos int
}

func lex(src string) ([]tok, error) {
	var out []tok
	i := 0
	for i < len(src) {
		c := rune(src[i])
		switch {
		case unicode.IsSpace(c):
			i++
		case c == '(' || c == ')':
			out = append(out, tok{string(c), i})
			i++
		case strings.ContainsRune("=!<>", c):
			j := i + 1
			if j < len(src) && src[j] == '=' {
				j++
			}
			op := src[i:j]
			if op == "=" || op == "!" {
				return nil, fmt.Errorf("column %d: unknown operator %q (use == or !=)", i+1, op)
			}
			out = append(out, tok{op, i})
			i = j
		default:
			j := i
			for j < len(src) && !unicode.IsSpace(rune(src[j])) && !strings.ContainsRune("()=!<>", rune(src[j])) {
				j++
			}
			out = append(out, tok{src[i:j], i})
			i = j
		}
	}
	return out, nil
}

type parser struct {
	toks []tok
	i    int
}

func (p *parser) peek() string {
	if p.i < len(p.toks) {
		return p.toks[p.i].s
	}
	return ""
}

func (p *parser) next() tok {
	t := p.toks[p.i]
	p.i++
	return t
}

// ParseExpr parses an alert condition.
func ParseExpr(src string) (*Expr, error) {
	toks, err := lex(src)
	if err != nil {
		return nil, err
	}
	if len(toks) == 0 {
		return nil, fmt.Errorf("empty condition")
	}
	p := &parser{toks: toks}
	e, err := p.or()
	if err != nil {
		return nil, err
	}
	if p.peek() == "for" {
		p.next()
		f, err := p.forClause()
		if err != nil {
			return nil, err
		}
		e.For = f
	}
	if p.i < len(p.toks) {
		t := p.toks[p.i]
		return nil, fmt.Errorf("column %d: unexpected %q", t.pos+1, t.s)
	}
	return e, nil
}

func (p *parser) forClause() (*For, error) {
	if p.peek() == "" {
		return nil, fmt.Errorf("expected a duration after 'for' (e.g. for 2m, for 3 samples)")
	}
	t := p.next()
	if p.peek() == "samples" || p.peek() == "times" {
		p.next()
		n, err := strconv.Atoi(t.s)
		if err != nil || n < 1 {
			return nil, fmt.Errorf("column %d: expected a positive number of samples", t.pos+1)
		}
		return &For{Samples: n}, nil
	}
	d, err := ParseDuration(t.s)
	if err != nil {
		return nil, fmt.Errorf("column %d: %v", t.pos+1, err)
	}
	return &For{Seconds: d.Seconds()}, nil
}

func (p *parser) or() (*Expr, error) {
	l, err := p.and()
	if err != nil {
		return nil, err
	}
	for p.peek() == "or" {
		p.next()
		r, err := p.and()
		if err != nil {
			return nil, err
		}
		l = &Expr{Op: "or", Args: []*Expr{l, r}}
	}
	return l, nil
}

func (p *parser) and() (*Expr, error) {
	l, err := p.unary()
	if err != nil {
		return nil, err
	}
	for p.peek() == "and" {
		p.next()
		r, err := p.unary()
		if err != nil {
			return nil, err
		}
		l = &Expr{Op: "and", Args: []*Expr{l, r}}
	}
	return l, nil
}

func (p *parser) unary() (*Expr, error) {
	switch p.peek() {
	case "":
		return nil, fmt.Errorf("unexpected end of condition")
	case "not":
		p.next()
		e, err := p.unary()
		if err != nil {
			return nil, err
		}
		return &Expr{Op: "not", Args: []*Expr{e}}, nil
	case "(":
		p.next()
		e, err := p.or()
		if err != nil {
			return nil, err
		}
		if p.peek() != ")" {
			return nil, fmt.Errorf("missing ')'")
		}
		p.next()
		return e, nil
	}
	return p.cmp()
}

func (p *parser) cmp() (*Expr, error) {
	t := p.next()
	ref, err := parseRef(t.s)
	if err != nil {
		return nil, fmt.Errorf("column %d: %v", t.pos+1, err)
	}
	op := p.peek()
	switch op {
	case "==", "!=", ">", ">=", "<", "<=":
		p.next()
	default:
		return nil, fmt.Errorf("column %d: expected a comparison (==, !=, >, >=, <, <=) after %s", t.pos+len(t.s)+1, t.s)
	}
	if p.peek() == "" {
		return nil, fmt.Errorf("expected a value after %s", op)
	}
	vt := p.next()
	val, err := parseValue(vt.s)
	if err != nil {
		return nil, fmt.Errorf("column %d: %v", vt.pos+1, err)
	}
	ft := ref.FieldType()
	if ft == "" {
		kind := ref.Kind
		if ref.Check != "" {
			kind = "check"
		}
		return nil, fmt.Errorf("column %d: %s has no field %q (fields: %s)", t.pos+1, kind, ref.Field, strings.Join(RefFields(kind), ", "))
	}
	if ft != val.Type {
		return nil, fmt.Errorf("column %d: %s is a %s but %q is a %s", vt.pos+1, ref.Text, ft, vt.s, val.Type)
	}
	if (ft == "status" || ft == "bool") && op != "==" && op != "!=" {
		return nil, fmt.Errorf("column %d: use == or != with %s", t.pos+1, ref.Text)
	}
	return &Expr{Op: "cmp", Ref: ref, Cmp: op, Value: val}, nil
}

func parseRef(s string) (*Ref, error) {
	parts := strings.Split(s, ".")
	if len(parts) < 3 {
		return nil, fmt.Errorf("%q is not a reference; write <node|link|route|collector>.<id>.<field>", s)
	}
	r := &Ref{Kind: parts[0], ID: parts[1], Text: s}
	switch r.Kind {
	case "node", "link", "route", "collector":
	default:
		return nil, fmt.Errorf("unknown entity %q in %q (use node, link, route or collector)", r.Kind, s)
	}
	rest := parts[2:]
	if len(rest) == 3 && rest[0] == "check" && r.Kind != "collector" {
		r.Check, r.Field = rest[1], rest[2]
		return r, nil
	}
	if len(rest) != 1 {
		return nil, fmt.Errorf("%q: expected <kind>.<id>.<field> or <kind>.<id>.check.<check>.<field>", s)
	}
	r.Field = rest[0]
	return r, nil
}

var statusWords = map[string]bool{"ok": true, "degraded": true, "down": true, "unknown": true}

func parseValue(s string) (*Value, error) {
	if statusWords[s] {
		return &Value{Type: "status", Status: s, Text: s}, nil
	}
	if s == "true" || s == "false" {
		return &Value{Type: "bool", Bool: s == "true", Text: s}, nil
	}
	units := []struct {
		suffix string
		mul    float64
	}{
		{"Gbps", 1e9}, {"Mbps", 1e6}, {"Kbps", 1e3}, {"bps", 1},
		{"%", 1}, {"ms", 0.001}, {"s", 1}, {"m", 60}, {"h", 3600}, {"d", 86400},
	}
	for _, u := range units {
		if strings.HasSuffix(s, u.suffix) {
			n, err := strconv.ParseFloat(strings.TrimSuffix(s, u.suffix), 64)
			if err != nil {
				return nil, fmt.Errorf("invalid number %q", s)
			}
			return &Value{Type: "number", Number: n * u.mul, Text: s}, nil
		}
	}
	n, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid value %q (use ok/degraded/down/unknown, true/false or a number like 5s, 80%%, 10Mbps)", s)
	}
	return &Value{Type: "number", Number: n, Text: s}, nil
}

// Refs returns every reference in the expression.
func (e *Expr) Refs() []*Ref {
	if e == nil {
		return nil
	}
	if e.Ref != nil {
		return []*Ref{e.Ref}
	}
	var out []*Ref
	for _, a := range e.Args {
		out = append(out, a.Refs()...)
	}
	return out
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
