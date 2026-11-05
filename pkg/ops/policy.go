package ops

// NodePolicy is what the hub knows about ops on one topology node. It is part
// of the bundle that `vpnmap apply` sends; pkg/topology derives it from the
// node's `ops:` block and the rest of the map. The runner never sees it: the
// server enforces only its RunnerConfig.
type NodePolicy struct {
	Node string `json:"node"`
	// Runner: vpnmap-ops is (to be) installed on this server.
	Runner bool `json:"runner"`
	// Address is the default SSH host (first node address); the Ops panel may
	// override it.
	Address string `json:"address,omitempty"`
	// Config is the runner config the server should have; ConfigHash its hash.
	Config     *RunnerConfig `json:"config,omitempty"`
	ConfigHash string        `json:"config_hash,omitempty"`
	// Verify are the map ids every change on this node is checked against:
	// the node, its links and the routes through them, plus ops.verify.
	Verify []string `json:"verify"`
	// CutsUsers: a disruptive change here interrupts a route that serves a
	// critical (paying) squad.
	CutsUsers bool `json:"cuts_users"`
	// CutRoutes are those routes (shown in the approval message).
	CutRoutes []string `json:"cut_routes,omitempty"`
	// Scripts carry the hub-side extras of declared scripts.
	Scripts []ScriptPolicy `json:"scripts,omitempty"`
	// NeverRestart lists containers/units the owner marked never_restart.
	NeverRestart []string `json:"never_restart,omitempty"`
	// Remnawave: the panel node name and the allowed node actions.
	Remnawave *RemnawavePolicy `json:"remnawave,omitempty"`
}

// ScriptPolicy is the hub-side part of a script declaration.
type ScriptPolicy struct {
	ID    string `json:"id"`
	Title string `json:"title,omitempty"`
	// Disruptive defaults to true for change scripts.
	Disruptive bool `json:"disruptive"`
	// ReadBefore runs before the change; its JSON output may be used in the
	// rollback as {before.<field>}.
	ReadBefore *Call `json:"read_before,omitempty"`
	// Rollback undoes the change (nil: not reversible).
	Rollback *Call `json:"rollback,omitempty"`
	// Verify are extra map ids; "{param}" is replaced with the argument.
	Verify []string `json:"verify,omitempty"`
}

// Call is a script call with argument templates: a literal token, "{param}"
// (an argument of the action) or "{before.<field>}" (read_before output).
type Call struct {
	Script string            `json:"script" yaml:"script"`
	Args   map[string]string `json:"args,omitempty" yaml:"args,omitempty"`
}

// RemnawavePolicy allows Remnawave node actions for one panel node.
type RemnawavePolicy struct {
	Name    string   `json:"name"`    // node name in Remnawave (from its remnawave-node check)
	Actions []string `json:"actions"` // rw.node-restart | rw.node-disable | rw.node-enable
}
