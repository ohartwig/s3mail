// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// CacheMax is the upper bound of the content cache. A mailbox with many
// attachments should not fill the disk.
const CacheMax = 256 << 20 // 256 MB

// bodyCache keeps whole messages on disk.
//
// The index is local anyway - which is why "reload" costs nothing for unchanged
// messages. The content did not behave that way: opening a message a third time
// meant fetching it from S3 a third time, attachments and all.
//
// The key is the ETag, not the object key. A delivered message does not change
// any more, and moving between folders changes the key - but not the content.
// That way the entry survives a move, and an object that really did change drops
// out by itself.
type bodyCache struct {
	dir string
	// key encrypts every body on the way to disk. These files are whole
	// messages - including the ones that arrived client-side encrypted, which
	// s3mail has to decrypt in order to show them.
	key []byte
	mu  sync.Mutex
}

func newBodyCache(dir string, key []byte) *bodyCache {
	if dir == "" {
		return nil
	}
	if os.MkdirAll(dir, 0o700) != nil {
		return nil
	}
	return &bodyCache{dir: dir, key: key}
}

func (c *bodyCache) path(etag string) string {
	// The ETag can hold characters that have no business in a file name - so hash
	// instead of hope.
	sum := sha256.Sum256([]byte(etag))
	return filepath.Join(c.dir, hex.EncodeToString(sum[:16])+".eml")
}

func (c *bodyCache) read(etag string) ([]byte, bool) {
	if c == nil || etag == "" {
		return nil, false
	}
	p := c.path(etag)
	raw, err := os.ReadFile(p)
	if err != nil {
		return nil, false
	}
	b, err := open(c.key, raw)
	if err != nil {
		// Written with another key. Treat it as a miss and fetch again; the file
		// is replaced on the way out.
		return nil, false
	}
	// Touch the access time, so eviction hits the rarely used entries and not the
	// most recently written ones.
	n := time.Now()
	_ = os.Chtimes(p, n, n)
	return b, true
}

func (c *bodyCache) put(etag string, body []byte) {
	if c == nil || etag == "" || len(body) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	blob, err := seal(c.key, body)
	if err != nil {
		return
	}
	tmp := c.path(etag) + ".tmp"
	if os.WriteFile(tmp, blob, 0o600) != nil {
		return
	}
	if os.Rename(tmp, c.path(etag)) != nil {
		_ = os.Remove(tmp)
		return
	}
	c.cleanup()
}

// cleanup throws away the entries unused for longest, until the limit holds
// again.
func (c *bodyCache) cleanup() {
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return
	}
	type file struct {
		path  string
		size  int64
		older int64
	}
	var all []file
	var total int64
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || e.IsDir() {
			continue
		}
		all = append(all, file{filepath.Join(c.dir, e.Name()), info.Size(), info.ModTime().Unix()})
		total += info.Size()
	}
	if total <= CacheMax {
		return
	}
	sort.Slice(all, func(i, j int) bool { return all[i].older < all[j].older })
	for _, d := range all {
		if total <= CacheMax {
			return
		}
		if os.Remove(d.path) == nil {
			total -= d.size
		}
	}
}

// Clear throws the whole cache away.
func (c *bodyCache) Clear() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	_ = os.RemoveAll(c.dir)
	_ = os.MkdirAll(c.dir, 0o700)
}
