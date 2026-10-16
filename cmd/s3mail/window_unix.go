// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package main

import (
	"fmt"
	"os"
	"syscall"
)

// ownedByUsAlone says whether the directory belongs to the current user and
// nobody else may enter it. Both halves matter: a directory another user made
// is theirs however it is set, and one of ours that group or others may enter
// hands the browser profile to them.
func ownedByUsAlone(dir string, info os.FileInfo) error {
	if st, ok := info.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
		return fmt.Errorf("%s belongs to another user", dir)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s is open to other users (%v)", dir, info.Mode().Perm())
	}
	return nil
}
