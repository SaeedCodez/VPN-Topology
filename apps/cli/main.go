// Command vpnmap is the CLI of VPN Topology: it validates and applies the
// topology files and reads the same facts the board shows from the hub.
// Exit codes: 0 healthy, 1 degraded, 2 down, 3 invalid input.
package main

import (
	"errors"
	"fmt"
	"os"
)

// Exit codes shared by every command.
const (
	ExitOK       = 0
	ExitDegraded = 1
	ExitDown     = 2
	ExitInvalid  = 3
)

// exitError carries an exit code through cobra.
type exitError struct {
	code int
	msg  string
}

func (e *exitError) Error() string { return e.msg }

func exitWith(code int, format string, a ...any) error {
	return &exitError{code: code, msg: fmt.Sprintf(format, a...)}
}

func main() {
	root := newRootCmd()
	err := root.Execute()
	if err == nil {
		os.Exit(exitCode)
	}
	var ee *exitError
	if errors.As(err, &ee) {
		if ee.msg != "" {
			fmt.Fprintln(os.Stderr, ee.msg)
		}
		os.Exit(ee.code)
	}
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(ExitInvalid)
}

// exitCode lets a successful command report degraded/down state.
var exitCode = ExitOK
