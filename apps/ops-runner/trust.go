package main

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// trustedFile rejects a file that root (uid) does not own, that group or
// others can write, that is not a regular file (no symlinks), or whose parent
// directories could be swapped by someone else. The runner refuses to run if
// its binary, its config, sudoers or an action script fails this check, so
// nobody but root can widen what it does.
func trustedFile(path string, uid uint32) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", path)
	}
	if err := owned(path, fi, uid); err != nil {
		return err
	}
	for d := filepath.Dir(filepath.Clean(path)); ; d = filepath.Dir(d) {
		dfi, err := os.Lstat(d)
		if err != nil {
			return err
		}
		if err := trustedDir(d, dfi, uid); err != nil {
			return err
		}
		if d == "/" || d == "." {
			return nil
		}
	}
}

func trustedDir(path string, fi os.FileInfo, uid uint32) error {
	if !fi.IsDir() {
		return fmt.Errorf("%s is not a directory", path)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("%s: cannot read owner", path)
	}
	if st.Uid != uid && st.Uid != 0 {
		return fmt.Errorf("%s is owned by uid %d, not root", path, st.Uid)
	}
	// World-writable is fine only with the sticky bit (like /tmp).
	if fi.Mode().Perm()&0o022 != 0 && fi.Mode()&os.ModeSticky == 0 {
		return fmt.Errorf("%s is writable by group or others", path)
	}
	return nil
}

func owned(path string, fi os.FileInfo, uid uint32) error {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("%s: cannot read owner", path)
	}
	if st.Uid != uid {
		return fmt.Errorf("%s is owned by uid %d, not root", path, st.Uid)
	}
	if fi.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%s is writable by group or others", path)
	}
	return nil
}
