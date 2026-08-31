// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"cmp"
	"context"
	"maps"
	"slices"

	"git.ole-hartwig.eu/development/s3mail/s3mail/core"
)

// Where the UID design from IMAP.md meets the bucket.
//
// core/uid.go holds the rules and proves them; here they are put to work. Two
// places, and both had to wait until the rules were sure:
//
//   - after a refresh, every message that has no number in its folder gets one
//   - after a move, the number in the source folder is retired
//
// The order in which numbers are handed out is the whole point. IMAP wants them
// ascending by arrival, and two machines looking at the same bucket have to
// arrive at the same order or one of them hands out a number the other already
// used. So: sorted by LastModified, key on a tie - the same input on both
// machines produces the same op.
//
// This runs on every refresh even when nothing is new. That is cheap: AssignUIDs
// skips what is already numbered, and an op with no messages is never written.

// EnsureUIDs numbers everything in a folder that has no number yet.
//
// Called when somebody asks for the numbering of a folder - which today is
// nobody, and tomorrow is an IMAP server selecting it. Deliberately not on
// every refresh:
//
// Numbers cost. They live in the state document, which every machine downloads
// on every start, and they cost about fifteen bytes per message - measured, not
// guessed: five thousand messages produce seventy-four kilobytes of pure
// numbering. A mailbox whose owner never touches IMAP would carry that forever
// for nothing.
//
// Numbering on demand is also correct rather than merely cheap. A UID has to be
// stable from the moment a client first sees it, and no client has seen
// anything before it selects the folder. Handing out numbers into an empty room
// buys nothing and pays for it every time.
//
// The empty string is the inbox, as everywhere else here.
func (m *Mailbox) EnsureUIDs(ctx context.Context, folder string) error {
	return m.assignUIDs(ctx, &folder)
}

// EnsureAllUIDs numbers every folder. For the moment an IMAP server starts and
// wants to answer LIST with sensible counts.
func (m *Mailbox) EnsureAllUIDs(ctx context.Context) error {
	return m.assignUIDs(ctx, nil)
}

// assignUIDs does the work for one folder, or for all of them when only is nil.
func (m *Mailbox) assignUIDs(ctx context.Context, only *string) error {
	byFolder := map[string][]core.Message{}
	m.mu.RLock()
	for _, msg := range m.index {
		if only != nil && msg.Folder != *only {
			continue
		}
		byFolder[msg.Folder] = append(byFolder[msg.Folder], msg)
	}
	m.mu.RUnlock()

	// Stable order, so two machines write the same ops.
	folders := slices.Sorted(maps.Keys(byFolder))

	var ops []core.Op
	d := m.State.Data()
	for _, folder := range folders {
		msgs := byFolder[folder]
		// Ascending by arrival: that is what a UID is supposed to mean. Date
		// comes from LastModified and is RFC 3339 in UTC, so string order is
		// time order; the key breaks a tie, and it does so the same way on
		// every machine.
		slices.SortFunc(msgs, func(a, b core.Message) int {
			return cmp.Or(
				cmp.Compare(a.Date, b.Date),
				cmp.Compare(a.Key, b.Key),
			)
		})
		mids := make([]string, 0, len(msgs))
		for _, msg := range msgs {
			mids = append(mids, msg.Mid)
		}
		if op := core.AssignUIDs(d, folder, mids); len(op.Mids) > 0 {
			ops = append(ops, op)
		}
	}
	if len(ops) == 0 {
		return nil
	}
	return m.State.Mutate(ctx, ops...)
}

// retireUIDs takes messages out of a folder's numbering after they left it.
//
// IMAP never reuses a UID, and the folder state keeps Next where it is - so
// this frees the association, not the number. Without it a moved message would
// keep a number in a folder it is no longer in, and the next client to look
// there would be told about a message that is not present.
func (m *Mailbox) retireUIDs(ctx context.Context, folder string, mids []string) error {
	if len(mids) == 0 {
		return nil
	}
	return m.State.Mutate(ctx, core.Op{T: "uidretire", Folder: folder, Mids: mids})
}

// UID is what an IMAP server will ask for. It does not number on its own: a
// getter that writes to the bucket would be a surprise. Call EnsureUIDs first.
func (m *Mailbox) UID(folder, mid string) (uint32, bool) {
	return m.State.Data().UID(folder, mid)
}

// UIDNext and UIDValidity are the two numbers IMAP reports when a folder is
// selected.
func (m *Mailbox) UIDNext(folder string) uint32     { return m.State.Data().UIDNext(folder) }
func (m *Mailbox) UIDValidity(folder string) uint32 { return m.State.Data().UIDValidity(folder) }
