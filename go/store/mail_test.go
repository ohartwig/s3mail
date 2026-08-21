package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"s3mail/core"
)

func mailBauen(from, to, subject, body, date, mid string) []byte {
	return []byte("From: " + from + "\r\nTo: " + to + "\r\nSubject: " + subject +
		"\r\nDate: " + date + "\r\nMessage-ID: " + mid +
		"\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n" + body + "\r\n")
}

func postfachBauen(t *testing.T) (*fakeS3, *Mailbox) {
	t.Helper()
	f := neuerFake()
	f.objs["mail/m1"] = mailBauen("Anna <anna@kunde.de>", "post@firma.de",
		"Rechnung 1", "Anbei die Rechnung.", "Mon, 03 Aug 2026 09:00:00 +0000", "<m1@x>")
	f.objs["mail/m2"] = mailBauen("Shop <news@shop.io>", "post@firma.de",
		"Angebot", "Neu im Sortiment.", "Tue, 04 Aug 2026 09:00:00 +0000", "<m2@x>")
	f.objs["mail/archiv/alt1"] = mailBauen("Alt <alt@firma.de>", "post@firma.de",
		"Altes", "Alter Text.", "Wed, 01 Jul 2026 08:00:00 +0000", "<alt1@x>")
	f.objs["andere/nicht-meins"] = []byte("ausserhalb")
	m := NewMailbox(context.Background(), f, nil, "test-bucket", "mail/", t.TempDir(), true)
	return f, m
}

func TestIndexUndOrdner(t *testing.T) {
	ctx := context.Background()
	_, m := postfachBauen(t)
	erg, err := m.Refresh(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if erg.Neu != 3 {
		t.Errorf("%d neue Mails, erwartet 3", erg.Neu)
	}
	for _, msg := range m.Index() {
		if strings.HasPrefix(msg.Key, "andere/") {
			t.Errorf("Objekt ausserhalb des Prefix im Index: %s", msg.Key)
		}
	}
	if got := m.Index()[0].Subject; got != "Altes" {
		t.Errorf("Betreff nicht geparst: %q", got)
	}
	nach := map[string]core.FolderInfo{}
	for _, o := range m.Ordner() {
		nach[o.Name] = o
	}
	if nach[core.Inbox].Count != 2 || nach[core.Archive].Count != 1 {
		t.Errorf("Ordnerzaehler: %+v", nach)
	}
}

// TestZustandUndOpsSindKeineMail - sonst tauchen sie als Nachricht auf.
func TestZustandUndOpsSindKeineMail(t *testing.T) {
	ctx := context.Background()
	_, m := postfachBauen(t)
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
	for _, o := range m.Ordner() {
		if strings.HasPrefix(o.Name, ".") {
			t.Errorf("interner Ordner in der Seitenleiste: %s", o.Name)
		}
	}
}

func TestVerschiebenNimmtZustandMit(t *testing.T) {
	ctx := context.Background()
	f, m := postfachBauen(t)
	if _, err := m.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if err := m.State.Mutate(ctx,
		core.Op{T: "tags", Mids: []string{"m1"}, Add: []string{"wichtig"}},
		core.Op{T: "flags", Mids: []string{"m1"}, Read: core.Ptr(true), Star: core.Ptr(true)}); err != nil {
		t.Fatal(err)
	}
	erg, err := m.Move(ctx, []string{"mail/m1"}, core.Archive)
	if err != nil {
		t.Fatal(err)
	}
	if len(erg) != 1 || erg[0].NewKey != "mail/archiv/m1" {
		t.Fatalf("%+v", erg)
	}
	if _, da := f.objs["mail/m1"]; da {
		t.Error("Original nicht geloescht")
	}
	e := m.State.Get("m1")
	if !e.Read || !e.Star || !hat(e.Tags, "wichtig") {
		t.Errorf("Zustand nach dem Verschieben verloren: %+v", e)
	}
}

func TestVerschiebenErbtVerschluesselung(t *testing.T) {
	ctx := context.Background()
	f, m := postfachBauen(t)
	f.sse["mail/m1"] = CopyOpts{ServerSideEncryption: "aws:kms",
		SSEKMSKeyID: "arn:aws:kms:eu-central-1:1:key/abc", StorageClass: "STANDARD_IA"}
	if _, err := m.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Move(ctx, []string{"mail/m1"}, core.Archive); err != nil {
		t.Fatal(err)
	}
	neu := f.sse["mail/archiv/m1"]
	if neu.ServerSideEncryption != "aws:kms" || neu.SSEKMSKeyID == "" {
		t.Errorf("Verschluesselung nicht mitgenommen: %+v", neu)
	}
	if neu.StorageClass != "STANDARD_IA" {
		t.Errorf("Speicherklasse nicht mitgenommen: %+v", neu)
	}
}

func TestVerschiebenPruefungen(t *testing.T) {
	ctx := context.Background()
	_, m := postfachBauen(t)
	if _, err := m.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Move(ctx, []string{"mail/m1"}, "../boese"); err == nil {
		t.Error("Ordnername mit .. durchgelassen")
	}
	if _, err := m.Move(ctx, []string{"andere/nicht-meins"}, core.Archive); err == nil {
		t.Error("Key ausserhalb des Prefix durchgelassen")
	}
	if _, err := m.Move(ctx, []string{"mail/" + StateObject}, core.Archive); err == nil {
		t.Error("Snapshot verschiebbar")
	}
	// in denselben Ordner: uebersprungen, nicht kopiert
	erg, err := m.Move(ctx, []string{"mail/m1"}, core.Inbox)
	if err != nil || len(erg) != 1 || !erg[0].Skipped {
		t.Errorf("Verschieben in denselben Ordner: %+v %v", erg, err)
	}
}

func TestNamenskollision(t *testing.T) {
	ctx := context.Background()
	f, m := postfachBauen(t)
	f.objs["mail/archiv/m1"] = mailBauen("X <x@y.de>", "post@firma.de", "Kollision",
		"Text", "Thu, 05 Aug 2026 09:00:00 +0000", "<k@x>")
	if _, err := m.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	erg, err := m.Move(ctx, []string{"mail/m1"}, core.Archive)
	if err != nil {
		t.Fatal(err)
	}
	if erg[0].NewKey != "mail/archiv/m1-1" {
		t.Errorf("Kollision nicht entschaerft: %q", erg[0].NewKey)
	}
	if _, da := f.objs["mail/archiv/m1"]; !da {
		t.Error("bestehende Mail ueberschrieben")
	}
}

// TestLoeschenNurAusPapierkorb - die Pruefung sitzt im Store, nicht in der UI.
func TestLoeschenNurAusPapierkorb(t *testing.T) {
	ctx := context.Background()
	_, m := postfachBauen(t)
	if _, err := m.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Delete(ctx, []string{"mail/m1"}, false); !errors.Is(err, ErrNurAusPapierkorb) {
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

func TestLoeschenGesperrt(t *testing.T) {
	ctx := context.Background()
	f := neuerFake()
	f.objs["mail/trash/m1"] = mailBauen("a@b.de", "c@d.de", "x", "y",
		"Mon, 03 Aug 2026 09:00:00 +0000", "<m1@x>")
	m := NewMailbox(ctx, f, nil, "test-bucket", "mail/", t.TempDir(), false)
	if _, err := m.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Delete(ctx, []string{"mail/trash/m1"}, false); !errors.Is(err, ErrLoeschenGesperrt) {
		t.Errorf("--no-delete nicht durchgesetzt: %v", err)
	}
}

// TestVerschluesseltKeinTeilstueck - ein halbes Chiffrat laesst sich nicht
// entschluesseln, also muss der Range-GET wegfallen, sobald so ein Objekt auftaucht.
func TestVerschluesseltKeinTeilstueck(t *testing.T) {
	ctx := context.Background()
	plain, faelle := umschlaegeLaden(t)
	f := neuerFake()
	body, key := entpacken(t, faelle["gcm"])
	f.objs["mail/enc1"] = body
	f.meta["mail/enc1"] = faelle["gcm"].Meta

	m := NewMailbox(ctx, f, &fakeKMS{key: key}, "test-bucket", "mail/", t.TempDir(), true)
	roh, err := m.Fetch(ctx, "mail/enc1", HeaderChunk)
	if err != nil {
		t.Fatal(err)
	}
	if string(roh) != string(plain) {
		t.Error("Klartext weicht ab")
	}
	if !m.Verschluesselt() {
		t.Error("Postfach nicht als verschluesselt gemerkt")
	}
	// zweiter Zugriff darf gar keinen Range mehr schicken
	f.Aufrufe = nil
	if _, err := m.Fetch(ctx, "mail/enc1", HeaderChunk); err != nil {
		t.Fatal(err)
	}
	for _, a := range f.Aufrufe {
		if strings.Contains(a, "bytes=") {
			t.Errorf("Teilstueck trotz Verschluesselung angefordert: %s", a)
		}
	}
}

func TestVerschluesseltOhneRechteKipptNurDieseMail(t *testing.T) {
	ctx := context.Background()
	_, faelle := umschlaegeLaden(t)
	f := neuerFake()
	body, _ := entpacken(t, faelle["gcm"])
	f.objs["mail/enc1"] = body
	f.meta["mail/enc1"] = faelle["gcm"].Meta
	f.objs["mail/klar"] = mailBauen("a@b.de", "c@d.de", "Lesbar", "Text",
		"Mon, 03 Aug 2026 09:00:00 +0000", "<k@x>")

	m := NewMailbox(ctx, f, &fakeKMS{fehler: errors.New("AccessDenied")},
		"test-bucket", "mail/", t.TempDir(), true)
	if _, err := m.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	nach := map[string]core.Message{}
	for _, msg := range m.Index() {
		nach[msg.Mid] = msg
	}
	if nach["klar"].Subject != "Lesbar" {
		t.Errorf("lesbare Mail mitgerissen: %+v", nach["klar"])
	}
	if nach["enc1"].Subject != "(nicht lesbar)" {
		t.Errorf("unlesbare Mail nicht markiert: %+v", nach["enc1"])
	}
}

func TestRegelnBeimIndexieren(t *testing.T) {
	ctx := context.Background()
	_, m := postfachBauen(t)
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
	nach := map[string]core.Message{}
	for _, msg := range m.Index() {
		nach[msg.Mid] = msg
	}
	if nach["m2"].Folder != core.Archive {
		t.Errorf("Regel hat nicht verschoben: %+v", nach["m2"])
	}
	if !hat(m.State.Get("m2").Tags, "Werbung") {
		t.Error("Regel-Tag fehlt")
	}
	if nach["m1"].Folder != core.Inbox {
		t.Error("Regel hat eine unbeteiligte Mail angefasst")
	}
}

func TestZwischenspeicherSpartRequests(t *testing.T) {
	ctx := context.Background()
	f, m := postfachBauen(t)
	if _, err := m.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	f.Aufrufe = nil
	erg, err := m.Refresh(ctx) // nichts hat sich geaendert
	if err != nil {
		t.Fatal(err)
	}
	if erg.Neu != 0 {
		t.Errorf("%d Mails erneut geholt, erwartet 0", erg.Neu)
	}
	for _, a := range f.Aufrufe {
		if strings.HasPrefix(a, "get mail/m") {
			t.Errorf("Mail trotz gleichem ETag erneut geholt: %s", a)
		}
	}
}
