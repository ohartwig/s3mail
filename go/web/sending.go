// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package web

import (
	"context"
	"net/http"
	"sync"

	"s3mail/store"
)

// Unfinished sends, and the one question the bucket cannot answer itself.
//
// store.RecoverSends finishes everything it can on its own. What comes back is
// the case where nobody knows whether SES took the message - and that is a
// question for a person, asked once, at the top of the mailbox. It must not be
// asked by a dialog that steals the focus: the answer may need a look into the
// sent folder of the recipient, or a phone call.

type openSends struct {
	mu  sync.Mutex
	byA map[string][]store.Sending
}

func (o *openSends) set(accID string, list []store.Sending) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.byA == nil {
		o.byA = map[string][]store.Sending{}
	}
	if len(list) == 0 {
		delete(o.byA, accID)
		return
	}
	o.byA[accID] = list
}

func (o *openSends) get(accID string) []store.Sending {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.byA[accID]
}

func (o *openSends) drop(accID, key string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	rest := make([]store.Sending, 0, len(o.byA[accID]))
	for _, s := range o.byA[accID] {
		if s.Key != key {
			rest = append(rest, s)
		}
	}
	if len(rest) == 0 {
		delete(o.byA, accID)
		return
	}
	o.byA[accID] = rest
}

// RecoverSends runs the recovery for every mailbox and remembers what needs an
// answer. Called once at start, before the window opens: a question about a
// mail from yesterday should be there when somebody looks, not appear later.
func (s *Server) RecoverSends(ctx context.Context) {
	for i := range s.accounts {
		acc := &s.accounts[i]
		ask, err := acc.Mailbox.RecoverSends(ctx)
		if err != nil {
			continue // a mailbox that cannot be reached is a problem of its own
		}
		s.open.set(acc.ID, ask)
	}
}

// sendingRoutes answers the question and takes it off the list.
func (s *Server) sendingRoutes() {
	s.mux.HandleFunc("POST /api/sending/resolve", func(w http.ResponseWriter, r *http.Request) {
		acc, ok := s.pick(w, r)
		if !ok {
			return
		}
		var body struct {
			Key  string `json:"key"`
			Sent bool   `json:"sent"`
		}
		if err := readJSON(r, &body); err != nil {
			s.writeError(w, http.StatusBadRequest, s.text(r, "error.badJson"))
			return
		}
		// Only a key this server actually asked about. Otherwise the route would
		// be a way to have any object treated as a send marker.
		known := false
		for _, o := range s.open.get(acc.ID) {
			if o.Key == body.Key {
				known = true
				break
			}
		}
		if !known {
			s.writeError(w, http.StatusBadRequest, s.text(r, "error.badInput"))
			return
		}
		if err := acc.Mailbox.ResolveSending(r.Context(), body.Key, body.Sent); err != nil {
			s.translate(w, r, err)
			return
		}
		s.open.drop(acc.ID, body.Key)
		s.json(w, http.StatusOK, with(s.overview(r, acc), map[string]any{"ok": true}))
	})
}

// pendingSends is what the page renders as a banner. Never nil: `null.map(...)`
// ends the script.
func (s *Server) pendingSends(acc *Account) []map[string]any {
	list := s.open.get(acc.ID)
	out := make([]map[string]any, 0, len(list))
	for _, o := range list {
		out = append(out, map[string]any{
			"key": o.Key, "to": o.To, "subject": o.Subject, "started": o.Started,
		})
	}
	return out
}
