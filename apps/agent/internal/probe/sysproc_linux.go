package probe

import "syscall"

// childAttr puts sing-box in its own process group (so Stop can kill the
// group) and makes the kernel kill it if the agent dies first.
func childAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
}
