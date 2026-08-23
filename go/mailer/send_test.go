package mailer

import (
	"mime"
	"net/mail"
	"strings"
	"testing"
	"time"
)

func now() time.Time { return time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC) }

func parse(t *testing.T, raw []byte) *mail.Message {
	t.Helper()
	m, err := mail.ReadMessage(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatalf("the built message is not readable: %v\n%s", err, raw)
	}
	return m
}

// TestReplyKeepsTheThread - without In-Reply-To and References the reply opens
// a new thread in the recipient's mailbox. That never shows up while testing
// and shows up at the recipient immediately.
func TestReplyKeepsTheThread(t *testing.T) {
	n, err := Build(Draft{Mode: "reply", To: "kunde@x.de", Subject: "Re: Rechnung",
		Body: "Passt so."}, "support@firma.de",
		Original{MessageID: "<abc@x.de>", References: "<alt1@x.de> <alt2@x.de>"}, now())
	if err != nil {
		t.Fatal(err)
	}
	m := parse(t, n.Raw)
	if m.Header.Get("In-Reply-To") != "<abc@x.de>" {
		t.Errorf("In-Reply-To: %q", m.Header.Get("In-Reply-To"))
	}
	refs := m.Header.Get("References")
	if !strings.HasSuffix(refs, "<abc@x.de>") || !strings.Contains(refs, "<alt1@x.de>") {
		t.Errorf("References: %q", refs)
	}
	// on a new message both may be missing
	n, _ = Build(Draft{Mode: "new", To: "kunde@x.de", Body: "Hallo"},
		"support@firma.de", Original{MessageID: "<abc@x.de>"}, now())
	if parse(t, n.Raw).Header.Get("In-Reply-To") != "" {
		t.Error("a new message gets an In-Reply-To")
	}
}

func TestRecipientsAndSender(t *testing.T) {
	n, err := Build(Draft{To: `"Nachname, Vorname" <a@x.de>, b@y.de`, Cc: "c@z.de",
		Body: "x"}, "support@firma.de", Original{}, now())
	if err != nil {
		t.Fatal(err)
	}
	if len(n.To) != 3 {
		t.Errorf("Empfaenger: %v", n.To)
	}
	for _, a := range n.To {
		if strings.Contains(a, "<") || strings.Contains(a, " ") {
			t.Errorf("SES bekommt einen Anzeigenamen statt einer Adresse: %q", a)
		}
	}
	if n.From != "support@firma.de" {
		t.Errorf("sender: %q", n.From)
	}
}

func TestRequiredFields(t *testing.T) {
	if _, err := Build(Draft{To: "a@b.de", Body: "x"}, "", Original{}, now()); err != ErrNoSender {
		t.Errorf("without a sender: %v", err)
	}
	if _, err := Build(Draft{Body: "x"}, "a@b.de", Original{}, now()); err != ErrNoRecipient {
		t.Errorf("ohne Empfaenger: %v", err)
	}
	if _, err := Build(Draft{To: "das ist keine adresse", Body: "x"}, "a@b.de",
		Original{}, now()); err == nil {
		t.Error("kaputte Empfaengerliste durchgelassen")
	}
}

// TestSubjectWithUmlauts - raw in the header this would be an 8-bit character
// and, depending on the server, either refused or mangled.
func TestSubjectWithUmlauts(t *testing.T) {
	n, err := Build(Draft{To: "a@b.de", Subject: "Rückfrage über 89,€", Body: "x"},
		"support@firma.de", Original{}, now())
	if err != nil {
		t.Fatal(err)
	}
	raw := string(n.Raw)
	if strings.Contains(raw, "Rückfrage") {
		t.Error("Umlaut steht roh im Header")
	}
	if !strings.Contains(raw, "=?utf-8?") {
		t.Errorf("not RFC 2047 encoded:\n%s", raw[:200])
	}
	// and readable again
	m := parse(t, n.Raw)
	dec := decryptHeader(m.Header.Get("Subject"))
	if dec != "Rückfrage über 89,€" {
		t.Errorf("zurueckgelesen: %q", dec)
	}
}

func decryptHeader(s string) string {
	out, err := (&mime.WordDecoder{}).DecodeHeader(s)
	if err != nil {
		return s
	}
	return out
}

// TestForwardAttachesTheMail
func TestForwardAttachesTheMail(t *testing.T) {
	original := []byte("From: alt@x.de\r\nSubject: Original\r\n\r\nAlter Inhalt.\r\n")
	n, err := Build(Draft{Mode: "forward", Key: "mail/m1", To: "kollege@firma.de",
		Subject: "Fwd: Original", Body: "Siehe unten."},
		"support@firma.de", Original{Subject: "Original", Raw: original}, now())
	if err != nil {
		t.Fatal(err)
	}
	m := parse(t, n.Raw)
	if !strings.HasPrefix(m.Header.Get("Content-Type"), "multipart/mixed") {
		t.Fatalf("Content-Type: %q", m.Header.Get("Content-Type"))
	}
	raw := string(n.Raw)
	if !strings.Contains(raw, "message/rfc822") {
		t.Error("the attachment does not have the type message/rfc822")
	}
	if !strings.Contains(raw, "Original.eml") {
		t.Error("the attachment has no file name")
	}
	if !strings.Contains(raw, "Alter Inhalt.") {
		t.Error("the forwarded message is missing from the attachment")
	}
	if !strings.Contains(raw, "Siehe unten.") {
		t.Error("own text missing")
	}
}

func TestMessageIDIsUnique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		n, err := Build(Draft{To: "a@b.de", Body: "x"}, "support@firma.de",
			Original{}, now())
		if err != nil {
			t.Fatal(err)
		}
		id := parse(t, n.Raw).Header.Get("Message-Id")
		if seen[id] {
			t.Fatalf("Message-ID doppelt: %s", id)
		}
		if !strings.HasSuffix(id, "@firma.de>") {
			t.Errorf("the domain from the sender is missing: %s", id)
		}
		seen[id] = true
	}
}
