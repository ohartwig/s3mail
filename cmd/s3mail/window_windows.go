// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package main

import "os"

// ownedByUsAlone has nothing to check on Windows: os.UserCacheDir is
// %LocalAppData%, a directory the account owns by construction, and Unix
// permission bits mean nothing to NTFS.
func ownedByUsAlone(string, os.FileInfo) error { return nil }
