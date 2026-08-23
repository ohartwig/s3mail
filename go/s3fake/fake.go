// Package s3fake mimics as much of S3 as s3mail uses: prefix listing, ETags,
// metadata and server-side encryption. Meant for tests only - hence a package
// of its own, so none of it can wander into the binary.
package s3fake

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"s3mail/store"
)

// Fake mimics as much of S3 as s3mail uses - prefix listing, ETags, metadata,
// server-side encryption. The counterpart of FakeS3 in the Python test suite.
type Fake struct {
	mu      sync.Mutex
	Objs    map[string][]byte
	Meta    map[string]map[string]string
	SSE     map[string]store.CopyOpts
	CallLog []string
	PutErr  error // wenn gesetzt, scheitert jedes Put
	ListErr error // wenn gesetzt, scheitert jedes List
}

func New() *Fake {
	return &Fake{
		Objs: map[string][]byte{},
		Meta: map[string]map[string]string{},
		SSE:  map[string]store.CopyOpts{},
	}
}

func (f *Fake) etag(key string) string {
	h := 0
	for _, b := range f.Objs[key] {
		h = h*31 + int(b)
	}
	return fmt.Sprintf("%08x", h&0xffffffff)
}

func (f *Fake) record(op string) { f.CallLog = append(f.CallLog, op) }

func (f *Fake) List(_ context.Context, _, prefix string) ([]store.ObjectInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("list " + prefix)
	if f.ListErr != nil {
		return nil, f.ListErr
	}
	var out []store.ObjectInfo
	for k, v := range f.Objs {
		if strings.HasPrefix(k, prefix) {
			out = append(out, store.ObjectInfo{Key: k, ETag: f.etag(k), Size: int64(len(v)),
				LastModified: time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func (f *Fake) Get(_ context.Context, _, key, byteRange string) (store.Object, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("get " + key + " " + byteRange)
	body, da := f.Objs[key]
	if !da {
		return store.Object{}, store.ErrNotFound
	}
	if byteRange != "" {
		var a, b int
		if _, err := fmt.Sscanf(byteRange, "bytes=%d-%d", &a, &b); err == nil {
			if b+1 < len(body) {
				body = body[a : b+1]
			} else if a < len(body) {
				body = body[a:]
			}
		}
	}
	return store.Object{Body: append([]byte(nil), body...), ETag: f.etag(key),
		Meta: copyMeta(f.Meta[key])}, nil
}

func (f *Fake) Head(_ context.Context, _, key string) (store.Head, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("head " + key)
	body, da := f.Objs[key]
	if !da {
		return store.Head{}, store.ErrNotFound
	}
	o := f.SSE[key]
	return store.Head{Meta: copyMeta(f.Meta[key]), ContentLength: int64(len(body)),
		ETag: f.etag(key), ServerSideEncryption: o.ServerSideEncryption,
		SSEKMSKeyID: o.SSEKMSKeyID, BucketKeyEnabled: o.BucketKeyEnabled,
		StorageClass: o.StorageClass}, nil
}

func (f *Fake) Put(_ context.Context, _, key string, body []byte, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("put " + key)
	if f.PutErr != nil {
		return f.PutErr
	}
	f.Objs[key] = append([]byte(nil), body...)
	return nil
}

func (f *Fake) Delete(_ context.Context, _, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("delete " + key)
	delete(f.Objs, key)
	delete(f.Meta, key)
	delete(f.SSE, key)
	return nil
}

func (f *Fake) Copy(_ context.Context, _, src, dst string, o store.CopyOpts) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("copy " + src + " -> " + dst)
	body, da := f.Objs[src]
	if !da {
		return store.ErrNotFound
	}
	f.Objs[dst] = append([]byte(nil), body...)
	if m := f.Meta[src]; m != nil {
		f.Meta[dst] = copyMeta(m) // Krypto-Umschlag mitkopieren
	}
	f.SSE[dst] = o
	return nil
}

func (f *Fake) Keys(prefix string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for k := range f.Objs {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func (f *Fake) PutCount(part string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, a := range f.CallLog {
		if strings.HasPrefix(a, "put ") && strings.Contains(a, part) {
			n++
		}
	}
	return n
}

func copyMeta(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// Put stores an object without touching the call log.
func (f *Fake) Store(key string, body []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Objs[key] = append([]byte(nil), body...)
}

// SetMeta attaches user metadata to an object (that is where the crypto envelope lives).
func (f *Fake) SetMeta(key string, meta map[string]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Meta[key] = copyMeta(meta)
}

// SetSSE defines an object's server-side encryption.
func (f *Fake) SetSSE(key string, o store.CopyOpts) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.SSE[key] = o
}

// SSEOf reads it back - to check whether it survived a copy.
func (f *Fake) SSEOf(key string) store.CopyOpts {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.SSE[key]
}

// Has says whether the object exists.
func (f *Fake) Has(key string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, da := f.Objs[key]
	return da
}

// ClearCalls resets the recording.
func (f *Fake) ClearCalls() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.CallLog = nil
}

// Calls returns what has been recorded so far.
func (f *Fake) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.CallLog...)
}
