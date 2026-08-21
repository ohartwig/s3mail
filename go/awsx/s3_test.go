package awsx_test

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"

	"s3mail/awsx"
	"s3mail/store"
)

// s3Server spricht so viel vom S3-Protokoll, wie s3mail benutzt. Damit laeuft das
// echte AWS-SDK gegen einen Server, den der Test kontrolliert - geprueft wird also
// die Anfrage auf dem Draht, nicht ein nachgebauter Client.
type s3Server struct {
	mu       sync.Mutex
	objs     map[string][]byte
	meta     map[string]map[string]string
	kopfe    map[string]http.Header
	Anfragen []string
	SeiteMax int // erzwingt Blaettern
}

func neuerS3Server() *s3Server {
	return &s3Server{objs: map[string][]byte{}, meta: map[string]map[string]string{},
		kopfe: map[string]http.Header{}, SeiteMax: 1000}
}

type inhalt struct {
	Key          string `xml:"Key"`
	LastModified string `xml:"LastModified"`
	ETag         string `xml:"ETag"`
	Size         int64  `xml:"Size"`
}

type listErgebnis struct {
	XMLName     xml.Name `xml:"ListBucketResult"`
	Name        string   `xml:"Name"`
	Prefix      string   `xml:"Prefix"`
	KeyCount    int      `xml:"KeyCount"`
	IsTruncated bool     `xml:"IsTruncated"`
	NextToken   string   `xml:"NextContinuationToken,omitempty"`
	Contents    []inhalt `xml:"Contents"`
}

func (s *s3Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pfad := strings.TrimPrefix(r.URL.Path, "/")
	bucket, key, _ := strings.Cut(pfad, "/")
	s.Anfragen = append(s.Anfragen, fmt.Sprintf("%s %s", r.Method, r.URL.RequestURI()))
	_ = bucket

	switch {
	case r.Method == "GET" && r.URL.Query().Has("list-type"):
		s.listen(w, r)
	case r.Method == "GET":
		s.holen(w, r, key)
	case r.Method == "HEAD":
		s.kopf(w, key)
	case r.Method == "PUT" && r.Header.Get("x-amz-copy-source") != "":
		s.kopieren(w, r, key)
	case r.Method == "PUT":
		s.legen(w, r, key)
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

func (s *s3Server) listen(w http.ResponseWriter, r *http.Request) {
	prefix := r.URL.Query().Get("prefix")
	var keys []string
	for k := range s.objs {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	if tok := r.URL.Query().Get("continuation-token"); tok != "" {
		for i, k := range keys {
			if k == tok {
				keys = keys[i:]
				break
			}
		}
	}
	erg := listErgebnis{Name: "test-bucket", Prefix: prefix}
	for i, k := range keys {
		if i >= s.SeiteMax {
			erg.IsTruncated = true
			erg.NextToken = k
			break
		}
		erg.Contents = append(erg.Contents, inhalt{Key: k,
			LastModified: "2026-08-01T12:00:00.000Z",
			ETag:         `"` + s.etag(k) + `"`, Size: int64(len(s.objs[k]))})
	}
	erg.KeyCount = len(erg.Contents)
	w.Header().Set("Content-Type", "application/xml")
	_ = xml.NewEncoder(w).Encode(erg)
}

func (s *s3Server) holen(w http.ResponseWriter, r *http.Request, key string) {
	body, da := s.objs[key]
	if !da {
		s.fehler(w, 404, "NoSuchKey")
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
	s.metaKopf(w, key)
	w.Header().Set("ETag", `"`+s.etag(key)+`"`)
	_, _ = w.Write(body)
}

func (s *s3Server) kopf(w http.ResponseWriter, key string) {
	if _, da := s.objs[key]; !da {
		s.fehler(w, 404, "NotFound")
		return
	}
	s.metaKopf(w, key)
	for k, vs := range s.kopfe[key] {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.Header().Set("ETag", `"`+s.etag(key)+`"`)
	w.WriteHeader(200)
}

func (s *s3Server) metaKopf(w http.ResponseWriter, key string) {
	for k, v := range s.meta[key] {
		w.Header().Set("x-amz-meta-"+k, v)
	}
}

func (s *s3Server) legen(w http.ResponseWriter, r *http.Request, key string) {
	buf := make([]byte, r.ContentLength)
	if r.ContentLength > 0 {
		_, _ = r.Body.Read(buf)
	}
	s.objs[key] = buf
	w.Header().Set("ETag", `"`+s.etag(key)+`"`)
	w.WriteHeader(200)
}

func (s *s3Server) kopieren(w http.ResponseWriter, r *http.Request, key string) {
	quelle := r.Header.Get("x-amz-copy-source")
	if u, err := url.PathUnescape(quelle); err == nil {
		quelle = u
	}
	_, srcKey, _ := strings.Cut(strings.TrimPrefix(quelle, "/"), "/")
	body, da := s.objs[srcKey]
	if !da {
		s.fehler(w, 404, "NoSuchKey")
		return
	}
	s.objs[key] = body
	if m := s.meta[srcKey]; m != nil {
		s.meta[key] = m
	}
	// merken, welche Kopfzeilen die Kopie mitbekommen hat
	kopf := http.Header{}
	for _, name := range []string{"x-amz-server-side-encryption",
		"x-amz-server-side-encryption-aws-kms-key-id",
		"x-amz-server-side-encryption-bucket-key-enabled", "x-amz-storage-class"} {
		if v := r.Header.Get(name); v != "" {
			kopf.Set(name, v)
		}
	}
	s.kopfe[key] = kopf
	w.Header().Set("Content-Type", "application/xml")
	_, _ = w.Write([]byte(`<CopyObjectResult><ETag>"x"</ETag></CopyObjectResult>`))
}

func (s *s3Server) fehler(w http.ResponseWriter, code int, kode string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(code)
	_, _ = w.Write([]byte("<Error><Code>" + kode + "</Code><Message>weg</Message></Error>"))
}

func (s *s3Server) kopfVon(key, name string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.kopfe[key].Get(name)
}

func (s *s3Server) mitschnitt() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.Anfragen...)
}

func adapter(t *testing.T, srv *s3Server) (*awsx.S3, *httptest.Server) {
	t.Helper()
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	cfg := aws.Config{
		Region:      "eu-central-1",
		Credentials: credentials.NewStaticCredentialsProvider("AKIATEST", "geheim", ""),
	}
	return awsx.NeuS3(cfg, ts.URL), ts
}

func TestListBlaettert(t *testing.T) {
	srv := neuerS3Server()
	srv.SeiteMax = 2 // erzwingt drei Seiten
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
		t.Fatalf("%d Objekte, erwartet 5 - blaettert der Paginator?", len(objs))
	}
	for _, o := range objs {
		if strings.HasPrefix(o.ETag, `"`) {
			t.Errorf("ETag nicht entklammert: %q", o.ETag)
		}
		if o.Size != 6 || o.LastModified.IsZero() {
			t.Errorf("Felder unvollstaendig: %+v", o)
		}
	}
	seiten := 0
	for _, r := range srv.mitschnitt() {
		if strings.Contains(r, "list-type") {
			seiten++
		}
	}
	if seiten < 3 {
		t.Errorf("%d Listing-Anfragen - es sollte geblaettert werden", seiten)
	}
}

// TestRangeWirdGeschickt - der Index holt nur die ersten 64 KB. Faellt der
// Range-Header weg, laedt s3mail bei jedem Abgleich das ganze Postfach.
func TestRangeWirdGeschickt(t *testing.T) {
	srv := neuerS3Server()
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

// TestMetadatenKommenAn - dort steckt der Krypto-Umschlag. Gehen sie verloren,
// ist eine verschluesselte Mail nicht mehr zu oeffnen.
func TestMetadatenKommenAn(t *testing.T) {
	srv := neuerS3Server()
	srv.objs["mail/enc"] = []byte("chiffrat")
	srv.meta["mail/enc"] = map[string]string{
		"x-amz-key-v2": "AAAA", "x-amz-iv": "BBBB",
		"x-amz-cek-alg": "AES/GCM/NoPadding", "x-amz-matdesc": `{"a":"b"}`}
	a, _ := adapter(t, srv)

	obj, err := a.Get(context.Background(), "test-bucket", "mail/enc", "")
	if err != nil {
		t.Fatal(err)
	}
	if !store.IstUmschlag(obj.Meta) {
		t.Fatalf("Umschlag nicht erkannt, Metadaten: %v", obj.Meta)
	}
	if store.KleineMeta(obj.Meta)["x-amz-matdesc"] != `{"a":"b"}` {
		t.Errorf("Encryption Context verloren: %v", obj.Meta)
	}
	h, err := a.Head(context.Background(), "test-bucket", "mail/enc")
	if err != nil {
		t.Fatal(err)
	}
	if !store.IstUmschlag(h.Meta) {
		t.Errorf("Head liefert keine Metadaten: %v", h.Meta)
	}
}

// TestKopierenNimmtVerschluesselungMit - sonst landet die Kopie unter dem
// Standardschluessel des Buckets statt unter dem des Originals.
func TestKopierenNimmtVerschluesselungMit(t *testing.T) {
	srv := neuerS3Server()
	srv.objs["mail/m1"] = []byte("inhalt")
	a, _ := adapter(t, srv)

	err := a.Copy(context.Background(), "test-bucket", "mail/m1", "mail/archiv/m1",
		store.CopyOpts{ServerSideEncryption: "aws:kms",
			SSEKMSKeyID:      "arn:aws:kms:eu-central-1:1:key/abc",
			BucketKeyEnabled: true, StorageClass: "STANDARD_IA"})
	if err != nil {
		t.Fatal(err)
	}
	pruef := map[string]string{
		"x-amz-server-side-encryption":                    "aws:kms",
		"x-amz-server-side-encryption-aws-kms-key-id":     "arn:aws:kms:eu-central-1:1:key/abc",
		"x-amz-server-side-encryption-bucket-key-enabled": "true",
		"x-amz-storage-class":                             "STANDARD_IA",
	}
	for name, want := range pruef {
		if got := srv.kopfVon("mail/archiv/m1", name); got != want {
			t.Errorf("%s: %q, erwartet %q", name, got, want)
		}
	}
}

// TestKopierenMitSonderzeichen - Schluessel mit Leerzeichen und Umlauten muessen
// in x-amz-copy-source kodiert werden, sonst schlaegt die Signatur fehl.
func TestKopierenMitSonderzeichen(t *testing.T) {
	srv := neuerS3Server()
	srv.objs["mail/Rechnung Übersicht.eml"] = []byte("inhalt")
	a, _ := adapter(t, srv)

	if err := a.Copy(context.Background(), "test-bucket",
		"mail/Rechnung Übersicht.eml", "mail/archiv/Rechnung Übersicht.eml",
		store.CopyOpts{}); err != nil {
		t.Fatal(err)
	}
	if _, da := srv.objs["mail/archiv/Rechnung Übersicht.eml"]; !da {
		t.Errorf("Kopie fehlt, vorhanden: %v", schluessel(srv))
	}
}

func TestFehlendesObjekt(t *testing.T) {
	srv := neuerS3Server()
	a, _ := adapter(t, srv)
	_, err := a.Get(context.Background(), "test-bucket", "mail/gibtsnicht", "")
	if !errors.Is(err, store.ErrNichtGefunden) {
		t.Errorf("NoSuchKey nicht uebersetzt: %v", err)
	}
	_, err = a.Head(context.Background(), "test-bucket", "mail/gibtsnicht")
	if !errors.Is(err, store.ErrNichtGefunden) {
		t.Errorf("NotFound nicht uebersetzt: %v", err)
	}
}

func TestPutUndDelete(t *testing.T) {
	srv := neuerS3Server()
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
	if _, da := srv.objs["mail/.s3mail-state/x.json"]; da {
		t.Error("nicht geloescht")
	}
}

// TestGanzesPostfachUeberDenAdapter faehrt die Schicht darueber gegen den
// S3-Server: indexieren, verschieben, Zustand schreiben und wieder lesen.
func TestGanzesPostfachUeberDenAdapter(t *testing.T) {
	ctx := context.Background()
	srv := neuerS3Server()
	srv.objs["mail/m1"] = []byte("From: Anna <anna@kunde.de>\r\nTo: post@firma.de\r\n" +
		"Subject: Rechnung\r\nDate: Mon, 03 Aug 2026 09:00:00 +0000\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n\r\nAnbei.\r\n")
	a, _ := adapter(t, srv)

	mb := store.NewMailbox(ctx, a, nil, "test-bucket", "mail/", t.TempDir(), true)
	if _, err := mb.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if len(mb.Index()) != 1 || mb.Index()[0].Subject != "Rechnung" {
		t.Fatalf("Index: %+v", mb.Index())
	}
	if _, err := mb.Move(ctx, []string{"mail/m1"}, "archiv"); err != nil {
		t.Fatal(err)
	}
	if _, da := srv.objs["mail/archiv/m1"]; !da {
		t.Errorf("nicht verschoben: %v", schluessel(srv))
	}
	// Zustand landet im Bucket und wird von einem zweiten Postfach gelesen
	zweites := store.NewMailbox(ctx, a, nil, "test-bucket", "mail/", t.TempDir(), true)
	if _, err := zweites.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if zweites.Index()[0].Folder != "archiv" {
		t.Errorf("zweiter Rechner sieht: %+v", zweites.Index()[0])
	}
}

func schluessel(s *s3Server) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for k := range s.objs {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
