// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package keyring

import (
	"os"
	"path/filepath"
	"syscall"
	"unsafe"

	"git.ole-hartwig.eu/development/s3mail/s3mail/config"
)

// Windows: DPAPI, and no dependency to get at it.
//
// There is no keychain to put an item into, but there is something better
// suited: CryptProtectData encrypts to the Windows account. The result can be
// written to an ordinary file - a copy of that file is useless on another
// machine and useless to another user on the same one, which is exactly the
// property the backup case needs.
//
// Reached through syscall.NewLazyDLL rather than golang.org/x/sys: the project
// takes the standard library plus the AWS SDK and go-message, and a whole
// dependency for two calls would not be a good trade.

var (
	crypt32            = syscall.NewLazyDLL("crypt32.dll")
	procProtectData    = crypt32.NewProc("CryptProtectData")
	procUnprotectData  = crypt32.NewProc("CryptUnprotectData")
	kernel32           = syscall.NewLazyDLL("kernel32.dll")
	procLocalFree      = kernel32.NewProc("LocalFree")
	cryptUIForbidFlags = uintptr(0x1) // CRYPTPROTECT_UI_FORBIDDEN
)

type dataBlob struct {
	cbData uint32
	pbData *byte
}

func newBlob(b []byte) dataBlob {
	if len(b) == 0 {
		return dataBlob{}
	}
	return dataBlob{cbData: uint32(len(b)), pbData: &b[0]}
}

// DPAPI is a Win32 API, and Go reaches a Win32 API through syscall: every
// argument crosses as a uintptr, and building one means unsafe.Pointer. There
// is no version of this file without it, which is why the findings below are
// silenced individually rather than the rule switched off - a real unsafe block
// somewhere else should still be caught.
func (b dataBlob) bytes() []byte {
	out := make([]byte, b.cbData)
	copy(out, unsafe.Slice(b.pbData, b.cbData)) //nosemgrep: go.lang.security.audit.unsafe.use-of-unsafe-block
	return out
}

func keyFile() string { return filepath.Join(config.Dir(), "cache.key") }

func load() ([]byte, error) {
	blob, err := os.ReadFile(keyFile())
	if os.IsNotExist(err) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, ErrUnavailable
	}
	in, out := newBlob(blob), dataBlob{}
	ok, _, _ := procUnprotectData.Call(uintptr(unsafe.Pointer(&in)), 0, 0, 0, 0, //nosemgrep: go.lang.security.audit.unsafe.use-of-unsafe-block
		cryptUIForbidFlags, uintptr(unsafe.Pointer(&out))) //nosemgrep: go.lang.security.audit.unsafe.use-of-unsafe-block
	if ok == 0 {
		// The file is there but cannot be decrypted: another user, another
		// machine, or a restored backup. Not an error to work around - the key
		// is gone, and a new one means a new cache.
		return nil, errNotFound
	}
	defer procLocalFree.Call(uintptr(unsafe.Pointer(out.pbData))) //nosemgrep: go.lang.security.audit.unsafe.use-of-unsafe-block
	return out.bytes(), nil
}

func store(key []byte) error {
	in, out := newBlob(key), dataBlob{}
	ok, _, _ := procProtectData.Call(uintptr(unsafe.Pointer(&in)), 0, 0, 0, 0, //nosemgrep: go.lang.security.audit.unsafe.use-of-unsafe-block
		cryptUIForbidFlags, uintptr(unsafe.Pointer(&out))) //nosemgrep: go.lang.security.audit.unsafe.use-of-unsafe-block
	if ok == 0 {
		return ErrUnavailable
	}
	defer procLocalFree.Call(uintptr(unsafe.Pointer(out.pbData))) //nosemgrep: go.lang.security.audit.unsafe.use-of-unsafe-block
	if err := os.MkdirAll(config.Dir(), 0o700); err != nil {
		return ErrUnavailable
	}
	if err := os.WriteFile(keyFile(), out.bytes(), 0o600); err != nil {
		return ErrUnavailable
	}
	return nil
}
