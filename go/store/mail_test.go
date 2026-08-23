package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"s3mail/core"
	"s3mail/s3fake"
	"s3mail/store"
)

func buildMail(from, to, subject, body, date, mid string) []byte {
	return []byte("From: " + from + "\r\nTo: " + to + "\r\nSubject: " + subject +
		"\r\nDate: " + date + "\r\nMessage-ID: " + mid +
		"\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n" + body + "\r\n")
}

func buildMailbox(t *testing.T) (*s3fake.Fake, *store.Mailbox) {
	t.Helper()
	f := s3fake.New()
	f.Objs["mail/m1"] = buildMail("Anna <anna@kunde.de>", "post@firma.de",
		"Rechnung 1", "Anbei die Rechnung.", "Mon, 03 Aug 2026 09:00:00 +0000", "<m1@x>")
	f.Objs["mail/m2"] = buildMail("Shop <news@shop.io>", "post@firma.de",
		"Angebot", "Neu im Sortiment.", "Tue, 04 Aug 2026 09:00:00 +0000", "<m2@x>")
	f.Objs["mail/archiv/alt1"] = buildMail("Alt <alt@firma.de>", "post@firma.de",
		"Altes", "Alter Text.", "Wed, 01 Jul 2026 08:00:00 +0000", "<alt1@x>")
	f.Objs["andere/nicht-meins"] = []byte("ausserhalb")
	m := store.NewMailbox(context.Background(), f, nil, "test-bucket", "mail/", t.TempDir(), true)
	return f, m
}

func TestIndexAndFolders(t *testing.T) {
	ctx := context.Background()
	_, m := buildMailbox(t)
	res, err := m.Refresh(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.New != 3 {
		t.Errorf("%d neue Mails, erwartet 3", res.New)
	}
	for _, msg := range m.Index() {
		if strings.HasPrefix(msg.Key, "andere/") {
			t.Errorf("Objekt ausserhalb des Prefix im Index: %s", msg.Key)
		}
	}
	if got := m.Index()[0].Subject; got != "Altes" {
		t.Errorf("Betreff nicht geparst: %q", got)
	}
	to := map[string]core.FolderInfo{}
	for _, o := range m.Folders() {
		to[o.Name] = o
	}
	if to[core.Inbox].Count != 2 || to[core.Archive].Count != 1 {
		t.Errorf("Ordnerzaehler: %+v", to)
	}
}

// TestStateAndOpsAreNotMail - sonst tauchen sie als Nachricht auf.
func TestStateAndOpsAreNotMail(t *testing.T) {
	ctx := context.Background()
	_, m := buildMailbox(t)
	if _, err := m.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if err := m.State.Mutate(ctx, core.Op{T: "flags", Mids: []string{"m1"}, Read: core.Ptr(true)}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	for _, msg := range m.Index() {
		if strings.Contains(msg.Key, "s3mail-state") {
			t.Errorf("interner Schluessel im Index: %s", msg.Key)
		}
	}
	for _, o := range m.Folders() {
		if strings.HasPrefix(o.Name, ".") {
			t.Errorf("interner Ordner in der Seitenleiste: %s", o.Name)
		}
	}
}

func TestMoveCarriesTheState(t *testing.T) {
	ctx := context.Background()
	f, m := buildMailbox(t)
	if _, err := m.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if err := m.State.Mutate(ctx,
		core.Op{T: "tags", Mids: []string{"m1"}, Add: []string{"wichtig"}},
		core.Op{T: "flags", Mids: []string{"m1"}, Read: core.Ptr(true), Star: core.Ptr(true)}); err != nil {
		t.Fatal(err)
	}
	res, err := m.Move(ctx, []string{"mail/m1"}, core.Archive)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].NewKey != "mail/archiv/m1" {
		t.Fatalf("%+v", res)
	}
	if _, da := f.Objs["mail/m1"]; da {
		t.Error("Original nicht geloescht")
	}
	e := m.State.Get("m1")
	if !e.Read || !e.Star || !has(e.Tags, "wichtig") {
		t.Errorf("Zustand nach dem Verschieben verloren: %+v", e)
	}
}

func TestMoveInheritsEncryption(t *testing.T) {
	ctx := context.Background()
	f, m := buildMailbox(t)
	f.SSE["mail/m1"] = store.CopyOpts{ServerSideEncryption: "aws:kms",
		SSEKMSKeyID: "arn:aws:kms:eu-central-1:1:key/abc", StorageClass: "STANDARD_IA"}
	if _, err := m.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Move(ctx, []string{"mail/m1"}, core.Archive); err != nil {
		t.Fatal(err)
	}
	fresh := f.SSE["mail/archiv/m1"]
	if fresh.ServerSideEncryption != "aws:kms" || fresh.SSEKMSKeyID == "" {
		t.Errorf("Verschluesselung nicht mitgenommen: %+v", fresh)
	}
	if fresh.StorageClass != "STANDARD_IA" {
		t.Errorf("Speicherklasse nicht mitgenommen: %+v", fresh)
	}
}

func TestMoveChecks(t *testing.T) {
	ctx := context.Background()
	_, m := buildMailbox(t)
	if _, err := m.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Move(ctx, []string{"mail/m1"}, "../boese"); err == nil {
		t.Error("Ordnername mit .. durchgelassen")
	}
	if _, err := m.Move(ctx, []string{"andere/nicht-meins"}, core.Archive); err == nil {
		t.Error("Key ausserhalb des Prefix durchgelassen")
	}
	if _, err := m.Move(ctx, []string{"mail/" + store.StateObject}, core.Archive); err == nil {
		t.Error("Snapshot verschiebbar")
	}
	// in denselben Ordner: uebersprungen, nicht kopiert
	res, err := m.Move(ctx, []string{"mail/m1"}, core.Inbox)
	if err != nil || len(res) != 1 || !res[0].Skipped {
		t.Errorf("Verschieben in denselben Ordner: %+v %v", res, err)
	}
}

func TestNameCollision(t *testing.T) {
	ctx := context.Background()
	f, m := buildMailbox(t)
	f.Objs["mail/archiv/m1"] = buildMail("X <x@y.de>", "post@firma.de", "Kollision",
		"Text", "Thu, 05 Aug 2026 09:00:00 +0000", "<k@x>")
	if _, err := m.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	res, err := m.Move(ctx, []string{"mail/m1"}, core.Archive)
	if err != nil {
		t.Fatal(err)
	}
	if res[0].NewKey != "mail/archiv/m1-1" {
		t.Errorf("Kollision nicht entschaerft: %q", res[0].NewKey)
	}
	if _, da := f.Objs["mail/archiv/m1"]; !da {
		t.Error("bestehende Mail ueberschrieben")
	}
}

// TestDeleteOnlyFromTrash - die Pruefung sitzt im Store, nicht in der UI.
func TestDeleteOnlyFromTrash(t *testing.T) {
	ctx := context.Background()
	_, m := buildMailbox(t)
	if _, err := m.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Delete(ctx, []string{"mail/m1"}, false); !errors.Is(err, store.ErrTrashOnly) {
		t.Errorf("Loeschen ausserhalb des Papierkorbs: %v", err)
	}
	if _, err := m.Move(ctx, []string{"mail/m1"}, core.Trash); err != nil {
		t.Fatal(err)
	}
	n, err := m.Delete(ctx, []string{"mail/trash/m1"}, false)
	if err != nil || n != 1 {
		t.Errorf("Loeschen aus dem Papierkorb: %d %v", n, err)
	}
	if _, da := m.State.Data().Messages["m1"]; da {
		t.Error("Zustandseintrag der geloeschten Mail blieb stehen")
	}
}

func TestDeleteBlocked(t *testing.T) {
	ctx := context.Background()
	f := s3fake.New()
	f.Objs["mail/trash/m1"] = buildMail("a@b.de", "c@d.de", "x", "y",
		"Mon, 03 Aug 2026 09:00:00 +0000", "<m1@x>")
	m := store.NewMailbox(ctx, f, nil, "test-bucket", "mail/", t.TempDir(), false)
	if _, err := m.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Delete(ctx, []string{"mail/trash/m1"}, false); !errors.Is(err, store.ErrDeleteBlocked) {
		t.Errorf("--no-delete nicht durchgesetzt: %v", err)
	}
}

// TestEncryptedMeansNoRangeGet - ein halbes Chiffrat laesst sich nicht
// entschluesseln, also muss der Range-GET wegfallen, sobald so ein Objekt auftaucht.
func TestEncryptedMeansNoRangeGet(t *testing.T) {
	ctx := context.Background()
	plain, cases := loadEnvelopes(t)
	f := s3fake.New()
	body, key := unpack(t, cases["gcm"])
	f.Objs["mail/enc1"] = body
	f.Meta["mail/enc1"] = cases["gcm"].Meta

	m := store.NewMailbox(ctx, f, &fakeKMS{key: key}, "test-bucket", "mail/", t.TempDir(), true)
	raw, err := m.Fetch(ctx, "mail/enc1", store.HeaderChunk)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != string(plain) {
		t.Error("Klartext weicht ab")
	}
	if !m.Encrypted() {
		t.Error("Postfach nicht als verschluesselt gemerkt")
	}
	// zweiter Zugriff darf gar keinen Range mehr schicken
	f.ClearCalls()
	if _, err := m.Fetch(ctx, "mail/enc1", store.HeaderChunk); err != nil {
		t.Fatal(err)
	}
	for _, a := range f.CallLog {
		if strings.Contains(a, "bytes=") {
			t.Errorf("Teilstueck trotz Verschluesselung angefordert: %s", a)
		}
	}
}

func TestEncryptedWithoutPermissionFailsOnlyThatMail(t *testing.T) {
	ctx := context.Background()
	_, cases := loadEnvelopes(t)
	f := s3fake.New()
	body, _ := unpack(t, cases["gcm"])
	f.Objs["mail/enc1"] = body
	f.Meta["mail/enc1"] = cases["gcm"].Meta
	f.Objs["mail/klar"] = buildMail("a@b.de", "c@d.de", "Lesbar", "Text",
		"Mon, 03 Aug 2026 09:00:00 +0000", "<k@x>")

	m := store.NewMailbox(ctx, f, &fakeKMS{err: errors.New("AccessDenied")},
		"test-bucket", "mail/", t.TempDir(), true)
	if _, err := m.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	to := map[string]core.Message{}
	for _, msg := range m.Index() {
		to[msg.Mid] = msg
	}
	if to["klar"].Subject != "Lesbar" {
		t.Errorf("lesbare Mail mitgerissen: %+v", to["klar"])
	}
	if to["enc1"].Subject != "(nicht lesbar)" {
		t.Errorf("unlesbare Mail nicht markiert: %+v", to["enc1"])
	}
}

func TestRulesWhileIndexing(t *testing.T) {
	ctx := context.Background()
	_, m := buildMailbox(t)
	archiv := core.Archive
	rules, err := core.CleanRules([]core.Rule{{Contains: "shop.io", Field: "from",
		Folder: &archiv, Tags: []string{"Werbung"}, Enabled: true}})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.State.Mutate(ctx, core.Op{T: "rules", Rules: rules}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ApplyRules(ctx, nil, false); err != nil {
		t.Fatal(err)
	}
	to := map[string]core.Message{}
	for _, msg := range m.Index() {
		to[msg.Mid] = msg
	}
	if to["m2"].Folder != core.Archive {
		t.Errorf("Regel hat nicht verschoben: %+v", to["m2"])
	}
	if !has(m.State.Get("m2").Tags, "Werbung") {
		t.Error("Regel-Tag fehlt")
	}
	if to["m1"].Folder != core.Inbox {
		t.Error("Regel hat eine unbeteiligte Mail angefasst")
	}
}

func TestCacheSavesRequests(t *testing.T) {
	ctx := context.Background()
	f, m := buildMailbox(t)
	if _, err := m.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	f.ClearCalls()
	res, err := m.Refresh(ctx) // nichts hat sich geaendert
	if err != nil {
		t.Fatal(err)
	}
	if res.New != 0 {
		t.Errorf("%d Mails erneut geholt, erwartet 0", res.New)
	}
	for _, a := range f.CallLog {
		if strings.HasPrefix(a, "get mail/m") {
			t.Errorf("Mail trotz gleichem ETag erneut geholt: %s", a)
		}
	}
}

// TestBodyFromTheCache - eine Mail zum zweiten Mal zu oeffnen darf
// keinen S3-Zugriff mehr kosten. Der Index lag schon immer lokal, der Inhalt
// nicht: bisher wurde jede geoeffnete Mail samt Anhaengen erneut geholt.
func TestBodyFromTheCache(t *testing.T) {
	ctx := context.Background()
	f, m := buildMailbox(t)
	if _, err := m.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	roh1, err := m.Fetch(ctx, "mail/m1", 0)
	if err != nil {
		t.Fatal(err)
	}
	f.ClearCalls()

	roh2, err := m.Fetch(ctx, "mail/m1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if string(roh1) != string(roh2) {
		t.Error("zwischengespeicherter Inhalt weicht ab")
	}
	for _, a := range f.Calls() {
		if strings.HasPrefix(a, "get mail/m1") {
			t.Errorf("trotz Zwischenspeicher erneut geholt: %s", a)
		}
	}
}

// TestCacheHangsOnTheETag - aendert sich das Objekt, muss der Eintrag
// verfallen. Sonst zeigt s3mail nach einem Wechsel des Inhalts die alte Fassung.
func TestCacheHangsOnTheETag(t *testing.T) {
	ctx := context.Background()
	f, m := buildMailbox(t)
	if _, err := m.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Fetch(ctx, "mail/m1", 0); err != nil {
		t.Fatal(err)
	}
	f.Store("mail/m1", buildMail("Neu <neu@x.de>", "post@firma.de", "Anderer Inhalt",
		"Voellig andere Mail.", "Fri, 07 Aug 2026 09:00:00 +0000", "<neu@x>"))
	if _, err := m.Refresh(ctx); err != nil { // neues ETag landet im Index
		t.Fatal(err)
	}
	raw, err := m.Fetch(ctx, "mail/m1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "Anderer Inhalt") {
		t.Error("alte Fassung aus dem Zwischenspeicher geliefert")
	}
}

// TestPartialFetchesAreNotCached - ein gespeichertes Teilstueck
// waere beim naechsten Oeffnen eine abgeschnittene Mail, ohne dass es auffaellt.
func TestPartialFetchesAreNotCached(t *testing.T) {
	ctx := context.Background()
	f, m := buildMailbox(t)
	if _, err := m.Refresh(ctx); err != nil { // holt nur HeaderChunk
		t.Fatal(err)
	}
	f.ClearCalls()
	if _, err := m.Fetch(ctx, "mail/m1", 0); err != nil {
		t.Fatal(err)
	}
	fetched := false
	for _, a := range f.Calls() {
		if strings.HasPrefix(a, "get mail/m1") {
			fetched = true
		}
	}
	if !fetched {
		t.Error("ganze Mail kam aus einem Teilstueck im Zwischenspeicher")
	}
}
