// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

//go:build !darwin && !windows && !linux

package keyring

// Everything else - the BSDs, plan9, whatever a Go release adds next. There is
// no place to keep a key that this package knows about, and guessing at one
// would be worse than saying so.

func load() ([]byte, error) { return nil, ErrUnavailable }
func store([]byte) error    { return ErrUnavailable }
