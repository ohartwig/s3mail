// Package s3fake bildet so viel von S3 nach, wie s3mail benutzt: Praefix-Listing,
// ETags, Metadaten und serverseitige Verschluesselung. Nur fuer Tests gedacht -
// deshalb ein eigenes Paket, damit nichts davon ins Binary wandert.
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

// fakeS3 bildet so viel von S3 nach, wie s3mail benutzt - Praefix-Listing, ETags,
// Metadaten, serverseitige Verschluesselung. Entspricht dem FakeS3 der
// Python-Testsuite.
type Fake struct {
	mu      sync.Mutex
	Objs    map[string][]byte
	Meta    map[string]map[string]string
	SSE     map[string]store.CopyOpts
	Aufrufe []string
	PutErr  error // wenn gesetzt, scheitert jedes Put
	ListErr error // wenn gesetzt, scheitert jedes List
}

func Neu() *Fake {
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

func (f *Fake) merken(was string) { f.Aufrufe = append(f.Aufrufe, was) }

func (f *Fake) List(_ context.Context, _, prefix string) ([]store.ObjectInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.merken("list " + prefix)
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
	f.merken("get " + key + " " + byteRange)
	body, da := f.Objs[key]
	if !da {
		return store.Object{}, store.ErrNichtGefunden
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
		Meta: kopieMeta(f.Meta[key])}, nil
}

func (f *Fake) Head(_ context.Context, _, key string) (store.Head, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.merken("head " + key)
	body, da := f.Objs[key]
	if !da {
		return store.Head{}, store.ErrNichtGefunden
	}
	o := f.SSE[key]
	return store.Head{Meta: kopieMeta(f.Meta[key]), ContentLength: int64(len(body)),
		ETag: f.etag(key), ServerSideEncryption: o.ServerSideEncryption,
		SSEKMSKeyID: o.SSEKMSKeyID, BucketKeyEnabled: o.BucketKeyEnabled,
		StorageClass: o.StorageClass}, nil
}

func (f *Fake) Put(_ context.Context, _, key string, body []byte, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.merken("put " + key)
	if f.PutErr != nil {
		return f.PutErr
	}
	f.Objs[key] = append([]byte(nil), body...)
	return nil
}

func (f *Fake) Delete(_ context.Context, _, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.merken("delete " + key)
	delete(f.Objs, key)
	delete(f.Meta, key)
	delete(f.SSE, key)
	return nil
}

func (f *Fake) Copy(_ context.Context, _, src, dst string, o store.CopyOpts) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.merken("copy " + src + " -> " + dst)
	body, da := f.Objs[src]
	if !da {
		return store.ErrNichtGefunden
	}
	f.Objs[dst] = append([]byte(nil), body...)
	if m := f.Meta[src]; m != nil {
		f.Meta[dst] = kopieMeta(m) // Krypto-Umschlag mitkopieren
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

func (f *Fake) PutZaehler(part string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, a := range f.Aufrufe {
		if strings.HasPrefix(a, "put ") && strings.Contains(a, part) {
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

// Setzen legt ein Objekt ab, ohne den Aufrufzaehler zu beruehren.
func (f *Fake) Setzen(key string, body []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Objs[key] = append([]byte(nil), body...)
}

// MetaSetzen haengt Nutzer-Metadaten an ein Objekt (dort steckt der Krypto-Umschlag).
func (f *Fake) MetaSetzen(key string, meta map[string]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Meta[key] = kopieMeta(meta)
}

// SSESetzen legt die serverseitige Verschluesselung eines Objekts fest.
func (f *Fake) SSESetzen(key string, o store.CopyOpts) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.SSE[key] = o
}

// SSEVon liest sie zurueck - fuer die Pruefung, ob sie beim Kopieren mitkam.
func (f *Fake) SSEVon(key string) store.CopyOpts {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.SSE[key]
}

// Hat sagt, ob es das Objekt gibt.
func (f *Fake) Hat(key string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, da := f.Objs[key]
	return da
}

// AufrufeLeeren setzt den Mitschnitt zurueck.
func (f *Fake) AufrufeLeeren() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Aufrufe = nil
}

// Mitschnitt liefert die bisherigen Aufrufe.
func (f *Fake) Mitschnitt() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.Aufrufe...)
}
