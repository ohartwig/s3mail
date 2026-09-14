// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"git.ole-hartwig.eu/development/s3mail/s3mail/go/core"
)

// Sending: the one place where a crash can cost a second mail.
//
// Everything else s3mail does is recoverable by looking at the bucket. Sending
// is not: SES has taken the message, and nothing in the bucket says so. If the
// program dies between the SES call and the copy into sent/, the next start
// sees a draft that was never sent - as far as it can tell - and the obvious
// repair is to send it again. To the recipient that is two identical mails,
// and if it was an invoice it is a phone call.
//
// So the intent is written down before SES is asked, and crossed out after
// everything that follows has happened. The marker lies next to the draft, in
// the same folder, under the same base name with .sending appended:
//
//	drafts/abc123.eml            the draft
//	drafts/abc123.eml.sending    the marker
//
// The order is what makes it work, and the reason for each step is that the
// step before it must already be recoverable:
//
//  1. write the marker            - without a SES id yet: "we are about to"
//  2. hand to SES                 - the irreversible one
//  3. write the SES id into it    - "it is out, id is this"
//  4. copy into sent/
//  5. drop the draft
//  6. delete the marker
//
// Which leaves exactly four states at the next start, and only one of them is
// a question for a human:
//
//	no marker            nothing happened, or everything did. Nothing to do.
//	marker without id    unknown. SES may or may not have taken it. Ask.
//	marker with id       out. Finish steps 4 to 6, quietly.
//	marker + no draft    same as above - the copy is rebuilt from the marker,
//	                     which is why the marker carries the message itself.
//
// The marker carries the raw message on purpose. Recovery must not depend on
// the draft still being there: step 5 may have run before step 6 died.

// sendingSuffix is appended to the draft's base name. It stays inside the
// drafts folder so that no new prefix appears in the bucket, and so that the
// IAM policy the wizard proposes keeps covering it. core knows the same string:
// it has to keep the marker out of the index.
const sendingSuffix = core.SendingSuffix

// Sending is a send that was started and not seen through.
type Sending struct {
	// Key is the marker object, not the draft.
	Key string `json:"key"`
	// SESMessageID is empty exactly when it is unknown whether SES took it.
	SESMessageID string `json:"ses_message_id,omitempty"`
	To           string `json:"to,omitempty"`
	Subject      string `json:"subject,omitempty"`
	Started      string `json:"started,omitempty"`
	// DraftKey is the draft this came from, empty if it was written fresh.
	DraftKey string `json:"draft_key,omitempty"`
	// MessageID is the mail's own Message-ID, needed to file the copy under the
	// same name a second attempt would use.
	MessageID string `json:"message_id,omitempty"`
	Raw       []byte `json:"raw,omitempty"`
}

// Decided is what the answer to the question can be.
func (s Sending) Decided() bool { return s.SESMessageID != "" }

// BeginSend writes step 1. The returned key goes back into FinishSend and
// AbandonSend; nothing else may touch it.
func (m *Mailbox) BeginSend(ctx context.Context, s Sending) (string, error) {
	base := baseFromID(s.MessageID)
	key, err := m.KeyFor(base+sendingSuffix, core.Drafts)
	if err != nil {
		return "", err
	}
	s.Key = key
	if s.Started == "" {
		s.Started = time.Now().UTC().Format(time.RFC3339)
	}
	blob, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	if err := m.s3.Put(ctx, m.bucket, key, blob, "application/json"); err != nil {
		return "", err
	}
	return key, nil
}

// MarkSent writes step 3: SES has taken it, and this is the id it gave.
//
// From here on nothing may report a failure upwards. The mail is out; an error
// after this point reads as "not sent" and gets it sent twice.
func (m *Mailbox) MarkSent(ctx context.Context, key, sesID string) error {
	s, err := m.readSending(ctx, key)
	if err != nil {
		return err
	}
	s.SESMessageID = sesID
	blob, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return m.s3.Put(ctx, m.bucket, key, blob, "application/json")
}

// AbandonSend removes the marker after everything downstream has happened - or
// when SES refused, in which case nothing went out and the marker would only
// raise a question that has no content.
func (m *Mailbox) AbandonSend(ctx context.Context, key string) error {
	if !strings.HasSuffix(key, sendingSuffix) {
		return fmt.Errorf("not a send marker: %s", key)
	}
	return m.s3.Delete(ctx, m.bucket, key)
}

// PendingSends lists the markers that survived. Sorted by key, so the answer is
// stable between two calls.
func (m *Mailbox) PendingSends(ctx context.Context) ([]Sending, error) {
	prefix, err := m.KeyFor("", core.Drafts)
	if err != nil {
		return nil, err
	}
	objs, err := m.s3.List(ctx, m.bucket, prefix)
	if err != nil {
		return nil, err
	}
	var out []Sending
	for _, o := range objs {
		if !strings.HasSuffix(o.Key, sendingSuffix) {
			continue
		}
		s, err := m.readSending(ctx, o.Key)
		if err != nil {
			continue // unreadable marker: better to skip than to block the mailbox
		}
		out = append(out, s)
	}
	slices.SortFunc(out, func(a, b Sending) int { return cmp.Compare(a.Key, b.Key) })
	return out, nil
}

// RecoverSends finishes what can be finished without asking, and returns what
// cannot.
//
// A marker that carries a SES id is not a question: the mail is out, and the
// only thing left is the bookkeeping - the copy into sent/, the draft, the
// marker itself. Doing that quietly is right, because the alternative is
// asking a person about something that has no alternative answer.
//
// A marker without an id is the one case where the bucket does not know. It
// comes back, and somebody has to say whether the mail went out.
func (m *Mailbox) RecoverSends(ctx context.Context) ([]Sending, error) {
	pending, err := m.PendingSends(ctx)
	if err != nil {
		return nil, err
	}
	var ask []Sending
	for _, s := range pending {
		if !s.Decided() {
			ask = append(ask, s)
			continue
		}
		if err := m.finishSent(ctx, s); err != nil {
			// Leave the marker where it is. The next start tries again; a marker
			// that cannot be cleared is better than a copy that never happens.
			continue
		}
	}
	return ask, nil
}

// ResolveSending is the answer to the question. sent=true means the mail went
// out and the copy is written; sent=false means it did not, and the draft stays
// where it is so it can be sent again deliberately.
func (m *Mailbox) ResolveSending(ctx context.Context, key string, sent bool) error {
	s, err := m.readSending(ctx, key)
	if err != nil {
		return err
	}
	if !sent {
		return m.AbandonSend(ctx, key)
	}
	s.SESMessageID = "unknown-confirmed-by-user"
	return m.finishSent(ctx, s)
}

// finishSent carries out steps 4 to 6. Every one of them is idempotent, so
// running it twice costs nothing.
func (m *Mailbox) finishSent(ctx context.Context, s Sending) error {
	if len(s.Raw) > 0 {
		if _, err := m.Put(ctx, core.Sent, s.MessageID, s.Raw); err != nil {
			return err
		}
	}
	if s.DraftKey != "" {
		// A draft that is already gone is not an error - that is the state we
		// are trying to reach.
		_ = m.DropDraft(ctx, s.DraftKey)
	}
	return m.AbandonSend(ctx, s.Key)
}

func (m *Mailbox) readSending(ctx context.Context, key string) (Sending, error) {
	obj, err := m.s3.Get(ctx, m.bucket, key, "")
	if err != nil {
		return Sending{}, err
	}
	var s Sending
	if err := json.Unmarshal(obj.Body, &s); err != nil {
		return Sending{}, err
	}
	s.Key = key
	return s, nil
}
