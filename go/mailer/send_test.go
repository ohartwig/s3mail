package mailer

import (
	"mime"
	"net/mail"
	"strings"
	"testing"
	"time"
)

func jetzt() time.Time { return time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC) }

func lies(t *testing.T, roh []byte) *mail.Message {
	t.Helper()
	m, err := mail.ReadMessage(strings.NewReader(string(roh)))
	if err != nil {
		t.Fatalf("gebaute Mail ist nicht lesbar: %v\n%s", err, roh)
	}
	return m
}

// TestAntwortHaeltDenFaden - ohne In-Reply-To und References macht die Antwort im
// Postfach des Empfaengers einen neuen Strang auf. Das faellt beim Testen nie auf
// und beim Empfaenger sofort.
func TestAntwortHaeltDenFaden(t *testing.T) {
	n, err := Bauen(Entwurf{Mode: "reply", To: "kunde@x.de", Subject: "Re: Rechnung",
		Body: "Passt so."}, "support@firma.de",
		Original{MessageID: "<abc@x.de>", References: "<alt1@x.de> <alt2@x.de>"}, jetzt())
	if err != nil {
		t.Fatal(err)
	}
	m := lies(t, n.Roh)
	if m.Header.Get("In-Reply-To") != "<abc@x.de>" {
		t.Errorf("In-Reply-To: %q", m.Header.Get("In-Reply-To"))
	}
	refs := m.Header.Get("References")
	if !strings.HasSuffix(refs, "<abc@x.de>") || !strings.Contains(refs, "<alt1@x.de>") {
		t.Errorf("References: %q", refs)
	}
	// bei einer neuen Mail darf beides fehlen
	n, _ = Bauen(Entwurf{Mode: "new", To: "kunde@x.de", Body: "Hallo"},
		"support@firma.de", Original{MessageID: "<abc@x.de>"}, jetzt())
	if lies(t, n.Roh).Header.Get("In-Reply-To") != "" {
		t.Error("neue Mail bekommt In-Reply-To")
	}
}

func TestEmpfaengerUndAbsender(t *testing.T) {
	n, err := Bauen(Entwurf{To: `"Nachname, Vorname" <a@x.de>, b@y.de`, Cc: "c@z.de",
		Body: "x"}, "support@firma.de", Original{}, jetzt())
	if err != nil {
		t.Fatal(err)
	}
	if len(n.Empfaenger) != 3 {
		t.Errorf("Empfaenger: %v", n.Empfaenger)
	}
	for _, a := range n.Empfaenger {
		if strings.Contains(a, "<") || strings.Contains(a, " ") {
			t.Errorf("SES bekommt einen Anzeigenamen statt einer Adresse: %q", a)
		}
	}
	if n.Absender != "support@firma.de" {
		t.Errorf("Absender: %q", n.Absender)
	}
}

func TestPflichtfelder(t *testing.T) {
	if _, err := Bauen(Entwurf{To: "a@b.de", Body: "x"}, "", Original{}, jetzt()); err != ErrKeinAbsender {
		t.Errorf("ohne Absender: %v", err)
	}
	if _, err := Bauen(Entwurf{Body: "x"}, "a@b.de", Original{}, jetzt()); err != ErrKeinEmpfaenger {
		t.Errorf("ohne Empfaenger: %v", err)
	}
	if _, err := Bauen(Entwurf{To: "das ist keine adresse", Body: "x"}, "a@b.de",
		Original{}, jetzt()); err == nil {
		t.Error("kaputte Empfaengerliste durchgelassen")
	}
}

// TestBetreffMitUmlauten - roh im Header waere das ein 8-Bit-Zeichen und je nach
// Server entweder abgelehnt oder verstuemmelt.
func TestBetreffMitUmlauten(t *testing.T) {
	n, err := Bauen(Entwurf{To: "a@b.de", Subject: "Rückfrage über 89,€", Body: "x"},
		"support@firma.de", Original{}, jetzt())
	if err != nil {
		t.Fatal(err)
	}
	roh := string(n.Roh)
	if strings.Contains(roh, "Rückfrage") {
		t.Error("Umlaut steht roh im Header")
	}
	if !strings.Contains(roh, "=?utf-8?") {
		t.Errorf("nicht RFC-2047-kodiert:\n%s", roh[:200])
	}
	// und wieder lesbar
	m := lies(t, n.Roh)
	dec := entschluesselnHeader(m.Header.Get("Subject"))
	if dec != "Rückfrage über 89,€" {
		t.Errorf("zurueckgelesen: %q", dec)
	}
}

func entschluesselnHeader(s string) string {
	out, err := (&mime.WordDecoder{}).DecodeHeader(s)
	if err != nil {
		return s
	}
	return out
}

// TestWeiterleitenHaengtDieMailAn
func TestWeiterleitenHaengtDieMailAn(t *testing.T) {
	original := []byte("From: alt@x.de\r\nSubject: Original\r\n\r\nAlter Inhalt.\r\n")
	n, err := Bauen(Entwurf{Mode: "forward", Key: "mail/m1", To: "kollege@firma.de",
		Subject: "Fwd: Original", Body: "Siehe unten."},
		"support@firma.de", Original{Subject: "Original", Roh: original}, jetzt())
	if err != nil {
		t.Fatal(err)
	}
	m := lies(t, n.Roh)
	if !strings.HasPrefix(m.Header.Get("Content-Type"), "multipart/mixed") {
		t.Fatalf("Content-Type: %q", m.Header.Get("Content-Type"))
	}
	roh := string(n.Roh)
	if !strings.Contains(roh, "message/rfc822") {
		t.Error("Anhang hat nicht den Typ message/rfc822")
	}
	if !strings.Contains(roh, "Original.eml") {
		t.Error("Dateiname des Anhangs fehlt")
	}
	if !strings.Contains(roh, "Alter Inhalt.") {
		t.Error("die weitergeleitete Mail fehlt im Anhang")
	}
	if !strings.Contains(roh, "Siehe unten.") {
		t.Error("eigener Text fehlt")
	}
}

func TestMessageIDIstEinmalig(t *testing.T) {
	gesehen := map[string]bool{}
	for i := 0; i < 100; i++ {
		n, err := Bauen(Entwurf{To: "a@b.de", Body: "x"}, "support@firma.de",
			Original{}, jetzt())
		if err != nil {
			t.Fatal(err)
		}
		id := lies(t, n.Roh).Header.Get("Message-Id")
		if gesehen[id] {
			t.Fatalf("Message-ID doppelt: %s", id)
		}
		if !strings.HasSuffix(id, "@firma.de>") {
			t.Errorf("Domain aus dem Absender fehlt: %s", id)
		}
		gesehen[id] = true
	}
}
