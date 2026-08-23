package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"s3mail/core"
	"s3mail/mailer"
	"s3mail/s3fake"
	"s3mail/store"
)

// fakeSender takes the message instead of SES and keeps it, so the test can look
// at what actually went out.
type fakeSender struct {
	sent []mailer.Message
	err  error
}

func (f *fakeSender) Send(_ context.Context, n mailer.Message) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	f.sent = append(f.sent, n)
	return "ses-id-1", nil
}

func serverWithSender(t *testing.T) (*httptest.Server, *store.Mailbox, *fakeSender) {
	t.Helper()
	ctx := context.Background()
	f := s3fake.New()
	f.Store("mail/m1", rawMail("Anna <anna@kunde.de>", "Rechnung 1", "Anbei die Rechnung.",
		"Mon, 03 Aug 2026 09:00:00 +0000"))
	mb := store.NewMailbox(ctx, f, nil, "test-bucket", "mail/", t.TempDir(), true)
	if _, err := mb.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	sender := &fakeSender{}
	accounts := one(mb)
	accounts[0].Sender = sender
	srv := NewServer(accounts, testToken, "127.0.0.1", 0, nil)
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	srv.Port = portOf(ts.URL)
	return ts, mb, sender
}

// TestSentMailIsKept is the hole this closes: until now a reply went to SES and
// was gone - no record of what anybody had written.
func TestSentMailIsKept(t *testing.T) {
	ts, mb, sender := serverWithSender(t)
	r := callServer(t, ts, "POST", "/api/send",
		`{"mode":"new","to":"kunde@x.de","subject":"Angebot","body":"Anbei."}`, nil)
	if r.Code != 200 {
		t.Fatalf("HTTP %d: %s", r.Code, r.Body)
	}
	var d struct {
		MessageID string `json:"message_id"`
		Warning   string `json:"warning"`
	}
	if err := json.Unmarshal(r.Body, &d); err != nil {
		t.Fatal(err)
	}
	if d.Warning != "" {
		t.Errorf("warning: %q", d.Warning)
	}
	if len(sender.sent) != 1 {
		t.Fatalf("%d messages handed to SES", len(sender.sent))
	}

	var sent int
	for _, m := range mb.Index() {
		if m.Folder != core.Sent {
			continue
		}
		sent++
		if m.Subject != "Angebot" {
			t.Errorf("subject of the copy: %q", m.Subject)
		}
		// Own mail counts as read - nobody needs a badge for what they wrote.
		if !mb.State.Get(m.Mid).Read {
			t.Error("the sent copy comes up unread")
		}
	}
	if sent != 1 {
		t.Fatalf("%d messages in the sent folder, expected 1", sent)
	}
}

// TestSendingSurvivesAFailedCopy - the message is out of the house. Turning the
// answer into an error would read as "not sent" and get it sent twice.
func TestSendingSurvivesAFailedCopy(t *testing.T) {
	ctx := context.Background()
	f := s3fake.New()
	mb := store.NewMailbox(ctx, f, nil, "test-bucket", "mail/", t.TempDir(), true)
	accounts := one(mb)
	accounts[0].Sender = &fakeSender{}
	srv := NewServer(accounts, testToken, "127.0.0.1", 0, nil)
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	srv.Port = portOf(ts.URL)

	f.PutErr = errors.New("no write permission")
	r := callServer(t, ts, "POST", "/api/send",
		`{"mode":"new","to":"kunde@x.de","subject":"x","body":"y"}`, nil)
	if r.Code != 200 {
		t.Fatalf("a failed copy turned into HTTP %d - that reads as \"not sent\"", r.Code)
	}
	if !strings.Contains(string(r.Body), "warning") {
		t.Errorf("no warning about the missing copy: %s", r.Body)
	}
}

// TestBccStaysOutOfTheMessage - it has to reach SES through the recipient list.
// In the header the blind copy would be in front of everybody's eyes.
func TestBccStaysOutOfTheMessage(t *testing.T) {
	ts, _, sender := serverWithSender(t)
	r := callServer(t, ts, "POST", "/api/send",
		`{"mode":"new","to":"a@x.de","bcc":"still@x.de","subject":"x","body":"y"}`, nil)
	if r.Code != 200 {
		t.Fatalf("HTTP %d: %s", r.Code, r.Body)
	}
	n := sender.sent[0]
	if strings.Contains(strings.ToLower(string(n.Raw)), "bcc:") {
		t.Error("Bcc header in the sent message")
	}
	if !strings.Contains(strings.Join(n.To, " "), "still@x.de") {
		t.Errorf("the blind copy is not among the recipients: %v", n.To)
	}
}

// TestAttachmentGoesOut - without this the only way to attach anything was to
// forward a message.
func TestAttachmentGoesOut(t *testing.T) {
	ts, _, sender := serverWithSender(t)
	body := `{"mode":"new","to":"a@x.de","subject":"x","body":"y","attachments":` +
		`[{"filename":"zahlen.csv","content_type":"text/csv","content":"YSxiLGMK"}]}`
	r := callServer(t, ts, "POST", "/api/send", body, nil)
	if r.Code != 200 {
		t.Fatalf("HTTP %d: %s", r.Code, r.Body)
	}
	raw := string(sender.sent[0].Raw)
	if !strings.Contains(raw, "multipart/mixed") {
		t.Error("no multipart message despite an attachment")
	}
	// The quotes around the file name are optional - mime.FormatMediaType leaves
	// them out where they change nothing. What has to be there is the name.
	if !strings.Contains(raw, "Content-Disposition: attachment") ||
		!strings.Contains(raw, "zahlen.csv") {
		t.Errorf("file name missing:\n%s", raw)
	}
	if !strings.Contains(raw, "base64") || !strings.Contains(raw, "YSxiLGMK") {
		t.Errorf("content not base64-encoded in the message:\n%s", raw)
	}
}

// TestDraftLandsInTheBucket - a draft is a real message in a real folder. That
// way it survives the closed window and a second machine sees it.
func TestDraftLandsInTheBucket(t *testing.T) {
	ts, mb, _ := serverWithSender(t)
	r := callServer(t, ts, "POST", "/api/draft",
		`{"to":"a@x.de","bcc":"still@x.de","subject":"halb fertig","body":"..."}`, nil)
	if r.Code != 200 {
		t.Fatalf("HTTP %d: %s", r.Code, r.Body)
	}
	var d struct {
		Key string `json:"key"`
	}
	_ = json.Unmarshal(r.Body, &d)
	if mb.FolderOf(d.Key) != core.Drafts {
		t.Fatalf("draft landed in %q", mb.FolderOf(d.Key))
	}
	raw, err := mb.Fetch(context.Background(), d.Key, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Here the Bcc has to stay: whoever reopens the draft must see whom they
	// meant to blind-copy.
	if !strings.Contains(string(raw), "Bcc: still@x.de") {
		t.Errorf("the draft lost the blind copy:\n%s", raw)
	}
}

// TestSecondSaveReplacesTheDraft - otherwise every keystroke round leaves
// another half-finished message behind.
func TestSecondSaveReplacesTheDraft(t *testing.T) {
	ts, mb, _ := serverWithSender(t)
	first := draftKey(t, ts, `{"to":"a@x.de","subject":"eins","body":"..."}`)
	second := draftKey(t, ts, `{"to":"a@x.de","subject":"zwei","body":"...","draft_key":"`+first+`"}`)
	if first == second {
		t.Fatal("the second save reused the key - then the two cannot be told apart")
	}
	n := 0
	for _, m := range mb.Index() {
		if m.Folder == core.Drafts {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d drafts after two saves, expected 1", n)
	}
}

// TestSendingRemovesTheDraft - the copy in the sent folder is the record; the
// draft next to it would be the same message a second time.
func TestSendingRemovesTheDraft(t *testing.T) {
	ts, mb, _ := serverWithSender(t)
	key := draftKey(t, ts, `{"to":"a@x.de","subject":"fertig","body":"..."}`)
	r := callServer(t, ts, "POST", "/api/send",
		`{"mode":"new","to":"a@x.de","subject":"fertig","body":"...","draft_key":"`+key+`"}`, nil)
	if r.Code != 200 {
		t.Fatalf("HTTP %d: %s", r.Code, r.Body)
	}
	for _, m := range mb.Index() {
		if m.Folder == core.Drafts {
			t.Errorf("draft still there after sending: %s", m.Key)
		}
	}
}

// TestDraftsGoDespiteNoDelete - --no-delete protects received mail from a wrong
// click. Our own scratch paper is a different matter: if it could not be
// removed, every sent message would leave its draft behind.
func TestDraftsGoDespiteNoDelete(t *testing.T) {
	ctx := context.Background()
	f := s3fake.New()
	mb := store.NewMailbox(ctx, f, nil, "test-bucket", "mail/", t.TempDir(), false)
	accounts := one(mb)
	accounts[0].Sender = &fakeSender{}
	srv := NewServer(accounts, testToken, "127.0.0.1", 0, nil)
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	srv.Port = portOf(ts.URL)

	key := draftKey(t, ts, `{"to":"a@x.de","subject":"x","body":"y"}`)
	if err := mb.DropDraft(ctx, key); err != nil {
		t.Fatalf("draft cannot be removed with --no-delete: %v", err)
	}
}

// TestOnlyDraftsCanBeDropped - the way past --no-delete must not become a way
// past it for received mail.
func TestOnlyDraftsCanBeDropped(t *testing.T) {
	_, mb, _ := serverWithSender(t)
	if err := mb.DropDraft(context.Background(), "mail/m1"); err == nil {
		t.Error("a message from the inbox was removed through the draft path")
	}
}

func draftKey(t *testing.T, ts *httptest.Server, body string) string {
	t.Helper()
	r := callServer(t, ts, "POST", "/api/draft", body, nil)
	if r.Code != 200 {
		t.Fatalf("HTTP %d: %s", r.Code, r.Body)
	}
	var d struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(r.Body, &d); err != nil {
		t.Fatal(err)
	}
	return d.Key
}
