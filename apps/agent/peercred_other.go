//go:build !linux

package main

import (
	"errors"
	"os"
)

// checkPeer: the helper only runs on Linux (systemd socket activation).
func checkPeer(*os.File) error { return errors.New("helper-serve is only supported on Linux") }
