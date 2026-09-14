package core

import (
	"maps"
	"slices"
)

// UIDs for IMAP - derived, never negotiated.
//
// IMAP demands a per-folder UID for every message: a 32-bit number, strictly
// increasing, stable forever. Handing out such a number is exactly the
// agreement the op log avoids everywhere else - two machines that see the same
// new message at the same time would have to settle who names it.
//
// They do not have to. Ops are replayed in the lexicographic order of their
// keys (store.listOps sorts them, and opName puts the timestamp first), and
// that order is the same on every machine. So "the first assignment for a
// message wins" already means "the op with the smaller key wins" - without
// Apply ever seeing a key. That is the whole trick, and it is why the rules
// below can be pure.
//
// What stays is the case the plan in IMAP.md admits to: two machines may hand
// the same number to different messages. Then the earlier op keeps the number,
// the later message stays unnumbered - it gets a fresh one on the next pass -
// and UIDVALIDITY of that folder goes up. A client that had already seen the
// losing assignment throws its cache away and resyncs. Once, and correctly.

// FolderUIDs is the UID state of a single folder.
//
// Next only ever grows. That is what makes a UID unretractable: a number handed
// out once is never given to a second message, not even after the first one was
// deleted or moved away.
type FolderUIDs struct {
	Validity uint32            `json:"validity"`
	Next     uint32            `json:"next"`
	UIDs     map[string]uint32 `json:"uids"`

	// owner is the reverse index, rebuilt on demand and never serialized -
	// derived data in the snapshot could disagree with the map above.
	owner map[uint32]string
}

func (f *FolderUIDs) normalize() *FolderUIDs {
	if f.UIDs == nil {
		f.UIDs = map[string]uint32{}
	}
	if f.Validity == 0 {
		f.Validity = 1
	}
	if f.Next == 0 {
		f.Next = 1 // UID 0 is not a valid IMAP UID
	}
	if f.owner == nil {
		f.owner = make(map[uint32]string, len(f.UIDs))
		for mid, n := range f.UIDs {
			f.owner[n] = mid
		}
	}
	return f
}

// folder returns the UID state of a folder, creating it on first use.
func (d *Data) folder(name string) *FolderUIDs {
	if d.UIDs == nil {
		d.UIDs = map[string]*FolderUIDs{}
	}
	f, ok := d.UIDs[name]
	if !ok || f == nil {
		f = &FolderUIDs{}
		d.UIDs[name] = f
	}
	return f.normalize()
}

// UID returns the number of a message in a folder.
func (d *Data) UID(folder, mid string) (uint32, bool) {
	if d.UIDs == nil {
		return 0, false
	}
	f, ok := d.UIDs[folder]
	if !ok || f == nil || f.UIDs == nil {
		return 0, false
	}
	n, ok := f.UIDs[mid]
	return n, ok
}

// UIDNext is what IMAP reports as UIDNEXT: the number the next message gets.
func (d *Data) UIDNext(folder string) uint32 { return d.folder(folder).Next }

// UIDValidity is IMAP's escape hatch. It rises whenever an assignment had to be
// rejected, and a client that sees it rise starts over.
func (d *Data) UIDValidity(folder string) uint32 { return d.folder(folder).Validity }

// AssignUIDs builds the op that numbers messages which have none yet.
//
// It is pure: nothing is written, nothing in d changes. The caller hands the
// basenames in the order they should be numbered - sorted by LastModified, key
// on a tie, so that two machines looking at the same bucket produce the same
// op. An empty op (no Mids) means there was nothing to do.
func AssignUIDs(d *Data, folder string, mids []string) Op {
	f := d.folder(folder)
	op := Op{T: "uid", Folder: folder}
	next := f.Next
	for _, mid := range mids {
		if _, have := f.UIDs[mid]; have {
			continue
		}
		op.Mids = append(op.Mids, mid)
		op.Nums = append(op.Nums, next)
		next++
	}
	return op
}

// applyUID is the convergent half of the design. Read the comment at the top of
// this file before changing any of the three branches.
func applyUID(d *Data, op Op) {
	f := d.folder(op.Folder)
	for i, mid := range op.Mids {
		if i >= len(op.Nums) {
			return // malformed op - nothing sensible left to do
		}
		n := op.Nums[i]
		if n == 0 {
			continue // 0 is not a UID; refuse it rather than hand it out
		}
		if have, ok := f.UIDs[mid]; ok {
			// The message is already numbered. An identical op is a replay and
			// harmless; a different number means an earlier op won, and the
			// message keeps what it has. Neither is a conflict.
			_ = have
			continue
		}
		if owner, taken := f.owner[n]; taken && owner != mid {
			// Two machines gave the same number to different messages. The
			// earlier op keeps it; this message stays unnumbered and gets a
			// fresh number on the next pass. Clients resync.
			f.Validity++
			continue
		}
		f.UIDs[mid] = n
		f.owner[n] = mid
		if n >= f.Next {
			f.Next = n + 1
		}
	}
}

// retireUID takes a message out of a folder's numbering - it left, by move or
// by deletion. Next stays where it is, so the number is gone for good.
func retireUID(d *Data, op Op) {
	f := d.folder(op.Folder)
	for _, mid := range op.Mids {
		if n, ok := f.UIDs[mid]; ok {
			delete(f.UIDs, mid)
			delete(f.owner, n)
		}
	}
}

// dropUIDs removes a message from every folder's numbering. Used by the "drop"
// op, which fires when an object has disappeared from the bucket entirely and
// therefore does not know which folder it was in.
func dropUIDs(d *Data, mids []string) {
	for name := range d.UIDs {
		f := d.folder(name)
		for _, mid := range mids {
			if n, ok := f.UIDs[mid]; ok {
				delete(f.UIDs, mid)
				delete(f.owner, n)
			}
		}
	}
}

// UIDFolders lists the folders that carry numbering, sorted - tests and the
// future server both want a stable order.
func (d *Data) UIDFolders() []string {
	return slices.Sorted(maps.Keys(d.UIDs))
}
