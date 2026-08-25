// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"sort"

	"s3mail/core"
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

// assignUIDs numbers everything in the index that has no number yet.
//
// It is called at the end of Refresh, deliberately after the index is complete:
// a message that arrived in this pass should get its number in this pass, or an
// IMAP client would see it appear without a UID and have to come back.
func (m *Mailbox) assignUIDs(ctx context.Context) error {
	byFolder := map[string][]core.Message{}
	m.mu.RLock()
	for _, msg := range m.index {
		byFolder[msg.Folder] = append(byFolder[msg.Folder], msg)
	}
	m.mu.RUnlock()

	folders := make([]string, 0, len(byFolder))
	for name := range byFolder {
		folders = append(folders, name)
	}
	sort.Strings(folders) // stable order, so two machines write the same ops

	var ops []core.Op
	d := m.State.Data()
	for _, folder := range folders {
		msgs := byFolder[folder]
		// Ascending by arrival: that is what a UID is supposed to mean. Date
		// comes from LastModified and is RFC 3339 in UTC, so string order is
		// time order; the key breaks a tie, and it does so the same way on
		// every machine.
		sort.Slice(msgs, func(i, j int) bool {
			if msgs[i].Date != msgs[j].Date {
				return msgs[i].Date < msgs[j].Date
			}
			return msgs[i].Key < msgs[j].Key
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

// UID is what an IMAP server will ask for. Here already, so that the numbering
// has a reader before the server exists - a number nobody can look up is a
// number nobody can check.
func (m *Mailbox) UID(folder, mid string) (uint32, bool) {
	return m.State.Data().UID(folder, mid)
}

// UIDNext and UIDValidity are the two numbers IMAP reports when a folder is
// selected.
func (m *Mailbox) UIDNext(folder string) uint32     { return m.State.Data().UIDNext(folder) }
func (m *Mailbox) UIDValidity(folder string) uint32 { return m.State.Data().UIDValidity(folder) }
