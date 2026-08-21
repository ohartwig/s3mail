package store

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// fakeS3 bildet so viel von S3 nach, wie s3mail benutzt - Praefix-Listing, ETags,
// Metadaten, serverseitige Verschluesselung. Entspricht dem FakeS3 der
// Python-Testsuite.
type fakeS3 struct {
	mu      sync.Mutex
	objs    map[string][]byte
	meta    map[string]map[string]string
	sse     map[string]CopyOpts
	Aufrufe []string
	PutErr  error // wenn gesetzt, scheitert jedes Put
}

func neuerFake() *fakeS3 {
	return &fakeS3{
		objs: map[string][]byte{},
		meta: map[string]map[string]string{},
		sse:  map[string]CopyOpts{},
	}
}

func (f *fakeS3) etag(key string) string {
	h := 0
	for _, b := range f.objs[key] {
		h = h*31 + int(b)
	}
	return fmt.Sprintf("%08x", h&0xffffffff)
}

func (f *fakeS3) merken(was string) { f.Aufrufe = append(f.Aufrufe, was) }

func (f *fakeS3) List(_ context.Context, _, prefix string) ([]ObjectInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.merken("list " + prefix)
	var out []ObjectInfo
	for k, v := range f.objs {
		if strings.HasPrefix(k, prefix) {
			out = append(out, ObjectInfo{Key: k, ETag: f.etag(k), Size: int64(len(v)),
				LastModified: time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func (f *fakeS3) Get(_ context.Context, _, key, byteRange string) (Object, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.merken("get " + key + " " + byteRange)
	body, da := f.objs[key]
	if !da {
		return Object{}, ErrNichtGefunden
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
	return Object{Body: append([]byte(nil), body...), ETag: f.etag(key),
		Meta: kopieMeta(f.meta[key])}, nil
}

func (f *fakeS3) Head(_ context.Context, _, key string) (Head, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.merken("head " + key)
	body, da := f.objs[key]
	if !da {
		return Head{}, ErrNichtGefunden
	}
	o := f.sse[key]
	return Head{Meta: kopieMeta(f.meta[key]), ContentLength: int64(len(body)),
		ETag: f.etag(key), ServerSideEncryption: o.ServerSideEncryption,
		SSEKMSKeyID: o.SSEKMSKeyID, BucketKeyEnabled: o.BucketKeyEnabled,
		StorageClass: o.StorageClass}, nil
}

func (f *fakeS3) Put(_ context.Context, _, key string, body []byte, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.merken("put " + key)
	if f.PutErr != nil {
		return f.PutErr
	}
	f.objs[key] = append([]byte(nil), body...)
	return nil
}

func (f *fakeS3) Delete(_ context.Context, _, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.merken("delete " + key)
	delete(f.objs, key)
	delete(f.meta, key)
	delete(f.sse, key)
	return nil
}

func (f *fakeS3) Copy(_ context.Context, _, src, dst string, o CopyOpts) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.merken("copy " + src + " -> " + dst)
	body, da := f.objs[src]
	if !da {
		return ErrNichtGefunden
	}
	f.objs[dst] = append([]byte(nil), body...)
	if m := f.meta[src]; m != nil {
		f.meta[dst] = kopieMeta(m) // Krypto-Umschlag mitkopieren
	}
	f.sse[dst] = o
	return nil
}

func (f *fakeS3) keys(prefix string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for k := range f.objs {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func (f *fakeS3) putZaehler(teil string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, a := range f.Aufrufe {
		if strings.HasPrefix(a, "put ") && strings.Contains(a, teil) {
			n++
		}
	}
	return n
}

func kopieMeta(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
