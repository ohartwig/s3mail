// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package awsx_test

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"

	"git.ole-hartwig.eu/development/s3mail/s3mail/awsx"
	"git.ole-hartwig.eu/development/s3mail/s3mail/store"
)

// s3Server speaks as much of the S3 protocol as s3mail uses. That way the real
// AWS SDK runs against a server the test controls - what gets checked is the
// request on the wire, not a rebuilt client.
type s3Server struct {
	mu       sync.Mutex
	objs     map[string][]byte
	meta     map[string]map[string]string
	headers  map[string]http.Header
	Requests []string
	pageMax  int // erzwingt Blaettern
}

func newS3Server() *s3Server {
	return &s3Server{objs: map[string][]byte{}, meta: map[string]map[string]string{},
		headers: map[string]http.Header{}, pageMax: 1000}
}

type content struct {
	Key          string `xml:"Key"`
	LastModified string `xml:"LastModified"`
	ETag         string `xml:"ETag"`
	Size         int64  `xml:"Size"`
}

type listResult struct {
	XMLName     xml.Name  `xml:"ListBucketResult"`
	Name        string    `xml:"Name"`
	Prefix      string    `xml:"Prefix"`
	KeyCount    int       `xml:"KeyCount"`
	IsTruncated bool      `xml:"IsTruncated"`
	NextToken   string    `xml:"NextContinuationToken,omitempty"`
	Contents    []content `xml:"Contents"`
}

func (s *s3Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := strings.TrimPrefix(r.URL.Path, "/")
	bucket, key, _ := strings.Cut(path, "/")
	s.Requests = append(s.Requests, fmt.Sprintf("%s %s", r.Method, r.URL.RequestURI()))
	_ = bucket

	switch {
	case r.Method == "GET" && r.URL.Query().Has("list-type"):
		s.handleList(w, r)
	case r.Method == "GET":
		s.handleGet(w, r, key)
	case r.Method == "HEAD":
		s.header(w, key)
	case r.Method == "PUT" && r.Header.Get("x-amz-copy-source") != "":
		s.handleCopy(w, r, key)
	case r.Method == "PUT":
		s.handlePut(w, r, key)
	case r.Method == "DELETE":
		delete(s.objs, key)
		w.WriteHeader(204)
	default:
		w.WriteHeader(400)
	}
}

func (s *s3Server) etag(key string) string {
	h := 0
	for _, b := range s.objs[key] {
		h = h*31 + int(b)
	}
	return fmt.Sprintf("%08x", h&0xffffffff)
}

func (s *s3Server) handleList(w http.ResponseWriter, r *http.Request) {
	prefix := r.URL.Query().Get("prefix")
	var keys []string
	for k := range s.objs {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	if tok := r.URL.Query().Get("continuation-token"); tok != "" {
		for i, k := range keys {
			if k == tok {
				keys = keys[i:]
				break
			}
		}
	}
	res := listResult{Name: "test-bucket", Prefix: prefix}
	for i, k := range keys {
		if i >= s.pageMax {
			res.IsTruncated = true
			res.NextToken = k
			break
		}
		res.Contents = append(res.Contents, content{Key: k,
			LastModified: "2026-08-01T12:00:00.000Z",
			ETag:         `"` + s.etag(k) + `"`, Size: int64(len(s.objs[k]))})
	}
	res.KeyCount = len(res.Contents)
	w.Header().Set("Content-Type", "application/xml")
	_ = xml.NewEncoder(w).Encode(res)
}

func (s *s3Server) handleGet(w http.ResponseWriter, r *http.Request, key string) {
	body, present := s.objs[key]
	if !present {
		s.fail(w, 404, "NoSuchKey")
		return
	}
	if rng := r.Header.Get("Range"); rng != "" {
		var a, b int
		if _, err := fmt.Sscanf(rng, "bytes=%d-%d", &a, &b); err == nil {
			if b+1 < len(body) {
				body = body[a : b+1]
			} else if a < len(body) {
				body = body[a:]
			}
		}
	}
	s.metaHeader(w, key)
	w.Header().Set("ETag", `"`+s.etag(key)+`"`)
	_, _ = w.Write(body)
}

func (s *s3Server) header(w http.ResponseWriter, key string) {
	if _, present := s.objs[key]; !present {
		s.fail(w, 404, "NotFound")
		return
	}
	s.metaHeader(w, key)
	for k, vs := range s.headers[key] {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.Header().Set("ETag", `"`+s.etag(key)+`"`)
	w.WriteHeader(200)
}

func (s *s3Server) metaHeader(w http.ResponseWriter, key string) {
	for k, v := range s.meta[key] {
		w.Header().Set("x-amz-meta-"+k, v)
	}
}

func (s *s3Server) handlePut(w http.ResponseWriter, r *http.Request, key string) {
	buf := make([]byte, r.ContentLength)
	if r.ContentLength > 0 {
		_, _ = r.Body.Read(buf)
	}
	s.objs[key] = buf
	w.Header().Set("ETag", `"`+s.etag(key)+`"`)
	w.WriteHeader(200)
}

func (s *s3Server) handleCopy(w http.ResponseWriter, r *http.Request, key string) {
	source := r.Header.Get("x-amz-copy-source")
	if u, err := url.PathUnescape(source); err == nil {
		source = u
	}
	_, srcKey, _ := strings.Cut(strings.TrimPrefix(source, "/"), "/")
	body, present := s.objs[srcKey]
	if !present {
		s.fail(w, 404, "NoSuchKey")
		return
	}
	s.objs[key] = body
	if m := s.meta[srcKey]; m != nil {
		s.meta[key] = m
	}
	// remember which headers the copy got
	header := http.Header{}
	for _, name := range []string{"x-amz-server-side-encryption",
		"x-amz-server-side-encryption-aws-kms-key-id",
		"x-amz-server-side-encryption-bucket-key-enabled", "x-amz-storage-class"} {
		if v := r.Header.Get(name); v != "" {
			header.Set(name, v)
		}
	}
	s.headers[key] = header
	w.Header().Set("Content-Type", "application/xml")
	_, _ = w.Write([]byte(`<CopyObjectResult><ETag>"x"</ETag></CopyObjectResult>`))
}

func (s *s3Server) fail(w http.ResponseWriter, awsCode int, code string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(awsCode)
	_, _ = w.Write([]byte("<Error><Code>" + code + "</Code><Message>weg</Message></Error>"))
}

func (s *s3Server) headerOf(key, name string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.headers[key].Get(name)
}

func (s *s3Server) calls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.Requests...)
}

func adapter(t *testing.T, srv *s3Server) (*awsx.S3, *httptest.Server) {
	t.Helper()
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	cfg := aws.Config{
		Region:      "eu-central-1",
		Credentials: credentials.NewStaticCredentialsProvider("AKIATEST", "geheim", ""),
	}
	return awsx.NewS3(cfg, ts.URL), ts
}

func TestListPaginates(t *testing.T) {
	srv := newS3Server()
	srv.pageMax = 2 // erzwingt drei Seiten
	for i := 0; i < 5; i++ {
		srv.objs[fmt.Sprintf("mail/m%d", i)] = []byte("inhalt")
	}
	srv.objs["andere/x"] = []byte("nicht meins")
	a, _ := adapter(t, srv)

	objs, err := a.List(context.Background(), "test-bucket", "mail/")
	if err != nil {
		t.Fatal(err)
	}
	if len(objs) != 5 {
		t.Fatalf("%d objects, expected 5 - does the paginator page?", len(objs))
	}
	for _, o := range objs {
		if strings.HasPrefix(o.ETag, `"`) {
			t.Errorf("ETag not unquoted: %q", o.ETag)
		}
		if o.Size != 6 || o.LastModified.IsZero() {
			t.Errorf("Felder unvollstaendig: %+v", o)
		}
	}
	pages := 0
	for _, r := range srv.calls() {
		if strings.Contains(r, "list-type") {
			pages++
		}
	}
	if pages < 3 {
		t.Errorf("%d listing requests - it should have paged", pages)
	}
}

// TestRangeIsSent - the index fetches only the first 64 KB. If the range
// header falls away, s3mail loads the whole mailbox on every refresh.
func TestRangeIsSent(t *testing.T) {
	srv := newS3Server()
	srv.objs["mail/m1"] = []byte("0123456789abcdefghij")
	a, _ := adapter(t, srv)

	obj, err := a.Get(context.Background(), "test-bucket", "mail/m1", "bytes=0-4")
	if err != nil {
		t.Fatal(err)
	}
	if string(obj.Body) != "01234" {
		t.Errorf("Teilstueck: %q", obj.Body)
	}
	obj, err = a.Get(context.Background(), "test-bucket", "mail/m1", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(obj.Body) != 20 {
		t.Errorf("ohne Range: %d Byte", len(obj.Body))
	}
}

// TestMetadataArrives - the crypto envelope sits there. If it gets lost, an
// encrypted message can no longer be opened.
func TestMetadataArrives(t *testing.T) {
	srv := newS3Server()
	srv.objs["mail/enc"] = []byte("chiffrat")
	srv.meta["mail/enc"] = map[string]string{
		"x-amz-key-v2": "AAAA", "x-amz-iv": "BBBB",
		"x-amz-cek-alg": "AES/GCM/NoPadding", "x-amz-matdesc": `{"a":"b"}`}
	a, _ := adapter(t, srv)

	obj, err := a.Get(context.Background(), "test-bucket", "mail/enc", "")
	if err != nil {
		t.Fatal(err)
	}
	if !store.IsEnvelope(obj.Meta) {
		t.Fatalf("envelope not recognised, metadata: %v", obj.Meta)
	}
	if store.LowerMeta(obj.Meta)["x-amz-matdesc"] != `{"a":"b"}` {
		t.Errorf("Encryption Context verloren: %v", obj.Meta)
	}
	h, err := a.Head(context.Background(), "test-bucket", "mail/enc")
	if err != nil {
		t.Fatal(err)
	}
	if !store.IsEnvelope(h.Meta) {
		t.Errorf("Head returns no metadata: %v", h.Meta)
	}
}

// TestCopyCarriesEncryption - otherwise the copy lands under the bucket's
// default key instead of the original's.
func TestCopyCarriesEncryption(t *testing.T) {
	srv := newS3Server()
	srv.objs["mail/m1"] = []byte("inhalt")
	a, _ := adapter(t, srv)

	err := a.Copy(context.Background(), "test-bucket", "mail/m1", "mail/archiv/m1",
		store.CopyOpts{ServerSideEncryption: "aws:kms",
			SSEKMSKeyID:      "arn:aws:kms:eu-central-1:1:key/abc",
			BucketKeyEnabled: true, StorageClass: "STANDARD_IA"})
	if err != nil {
		t.Fatal(err)
	}
	wants := map[string]string{
		"x-amz-server-side-encryption":                    "aws:kms",
		"x-amz-server-side-encryption-aws-kms-key-id":     "arn:aws:kms:eu-central-1:1:key/abc",
		"x-amz-server-side-encryption-bucket-key-enabled": "true",
		"x-amz-storage-class":                             "STANDARD_IA",
	}
	for name, want := range wants {
		if got := srv.headerOf("mail/archiv/m1", name); got != want {
			t.Errorf("%s: %q, expected %q", name, got, want)
		}
	}
}

// TestCopyWithSpecialCharacters - keys with spaces and umlauts have to be
// encoded in x-amz-copy-source, or the signature fails.
func TestCopyWithSpecialCharacters(t *testing.T) {
	srv := newS3Server()
	srv.objs["mail/Rechnung Übersicht.eml"] = []byte("inhalt")
	a, _ := adapter(t, srv)

	if err := a.Copy(context.Background(), "test-bucket",
		"mail/Rechnung Übersicht.eml", "mail/archiv/Rechnung Übersicht.eml",
		store.CopyOpts{}); err != nil {
		t.Fatal(err)
	}
	if _, present := srv.objs["mail/archiv/Rechnung Übersicht.eml"]; !present {
		t.Errorf("copy missing, present: %v", key(srv))
	}
}

func TestMissingObject(t *testing.T) {
	srv := newS3Server()
	a, _ := adapter(t, srv)
	_, err := a.Get(context.Background(), "test-bucket", "mail/gibtsnicht", "")
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("NoSuchKey not translated: %v", err)
	}
	_, err = a.Head(context.Background(), "test-bucket", "mail/gibtsnicht")
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("NotFound not translated: %v", err)
	}
}

func TestPutAndDelete(t *testing.T) {
	srv := newS3Server()
	a, _ := adapter(t, srv)
	ctx := context.Background()
	if err := a.Put(ctx, "test-bucket", "mail/.s3mail-state/x.json",
		[]byte(`{"ops":[]}`), "application/json"); err != nil {
		t.Fatal(err)
	}
	if string(srv.objs["mail/.s3mail-state/x.json"]) != `{"ops":[]}` {
		t.Errorf("Inhalt: %q", srv.objs["mail/.s3mail-state/x.json"])
	}
	if err := a.Delete(ctx, "test-bucket", "mail/.s3mail-state/x.json"); err != nil {
		t.Fatal(err)
	}
	if _, present := srv.objs["mail/.s3mail-state/x.json"]; present {
		t.Error("not deleted")
	}
}

// TestWholeMailboxThroughTheAdapter runs the layer above against the S3
// server: index, move, write the state and read it back.
func TestWholeMailboxThroughTheAdapter(t *testing.T) {
	ctx := context.Background()
	srv := newS3Server()
	srv.objs["mail/m1"] = []byte("From: Anna <anna@kunde.de>\r\nTo: post@firma.de\r\n" +
		"Subject: Rechnung\r\nDate: Mon, 03 Aug 2026 09:00:00 +0000\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n\r\nAnbei.\r\n")
	a, _ := adapter(t, srv)

	mb := store.NewMailbox(ctx, a, nil, "test-bucket", "mail/", t.TempDir(), testCacheKey, true)
	if _, err := mb.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if len(mb.Index()) != 1 || mb.Index()[0].Subject != "Rechnung" {
		t.Fatalf("Index: %+v", mb.Index())
	}
	if _, err := mb.Move(ctx, []string{"mail/m1"}, "archiv"); err != nil {
		t.Fatal(err)
	}
	if _, present := srv.objs["mail/archiv/m1"]; !present {
		t.Errorf("not moved: %v", key(srv))
	}
	// the state lands in the bucket and is read by a second mailbox
	other := store.NewMailbox(ctx, a, nil, "test-bucket", "mail/", t.TempDir(), testCacheKey, true)
	if _, err := other.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if other.Index()[0].Folder != "archiv" {
		t.Errorf("the second machine sees: %+v", other.Index()[0])
	}
}

func key(s *s3Server) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for k := range s.objs {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// TestBucketsWithoutPermissionYieldEmptyList checks the path that hit every
// mailbox user: their IAM policy deliberately grants no s3:ListAllMyBuckets,
// so the call runs into an AccessDenied.
//
// What matters is that an EMPTY slice comes out of it and not nil: a nil slice
// becomes JSON `null`, and the interface calls .map() on it. The reader then
// saw not "no right to list" but "Cannot read properties of null (reading
// 'map')" - and nothing worked any more.
func TestBucketsWithoutPermissionYieldEmptyList(t *testing.T) {
	srv := newS3Server()
	verweigern := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && r.URL.Path == "/" {
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(403)
			_, _ = w.Write([]byte("<Error><Code>AccessDenied</Code><Message>nope</Message></Error>"))
			return
		}
		srv.ServeHTTP(w, r)
	})
	ts := httptest.NewServer(verweigern)
	defer ts.Close()
	cfg := aws.Config{Region: "eu-north-1",
		Credentials: credentials.NewStaticCredentialsProvider("AKIATEST", "geheim", "")}
	a := awsx.NewS3(cfg, ts.URL)

	buckets, err := a.Buckets(context.Background())
	if err != nil {
		t.Fatalf("AccessDenied should not be an error: %v", err)
	}
	if buckets == nil {
		t.Fatal("nil instead of an empty list - the interface dies on this")
	}
	blob, _ := json.Marshal(map[string]any{"buckets": buckets})
	if string(blob) != `{"buckets":[]}` {
		t.Errorf("JSON: %s", blob)
	}
}
