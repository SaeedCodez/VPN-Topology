package main

import (
	"errors"
	"fmt"
	"os"
	"os/user"
	"strconv"
	"syscall"
)

// checkPeer verifies the connecting process is root or the agent user
// (SO_PEERCRED). The socket's 0660 root:vpnmap-agent mode already enforces
// this; checking again costs nothing. When stdin is not a socket the helper
// was started by hand by root, who needs no protection from itself.
func checkPeer(f *os.File) error {
	cred, err := syscall.GetsockoptUcred(int(f.Fd()), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	if err != nil {
		if errors.Is(err, syscall.ENOTSOCK) {
			return nil
		}
		return fmt.Errorf("cannot read peer credentials: %w", err)
	}
	if cred.Uid == 0 {
		return nil
	}
	if u, err := user.Lookup(agentUser); err == nil && u.Uid == strconv.Itoa(int(cred.Uid)) {
		return nil
	}
	return fmt.Errorf("uid %d is not %s", cred.Uid, agentUser)
}
