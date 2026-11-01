//go:build !linux

package probe

import "syscall"

// childAttr: the agent targets Linux; this only keeps the module building
// on developer machines.
func childAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setpgid: true} }
