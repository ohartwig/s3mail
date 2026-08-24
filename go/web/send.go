// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package web

import (
	"context"
	"net/http"
	"time"

	"s3mail/core"
	"s3mail/mailer"
	"s3mail/wizard"
)

// Sender is the slice of SES the server needs.
type Sender interface {
	Send(ctx context.Context, n mailer.Message) (string, error)
}

// WithWizard attaches the /api/setup/* routes.
func (s *Server) WithWizard(a *wizard.Wizard) { s.wizard = a }

func (s *Server) sendRoute() {
	s.mux.HandleFunc("POST /api/send", func(w http.ResponseWriter, r *http.Request) {
		acc, ok := s.pick(w, r)
		if !ok {
			return
		}
		if acc.Sender == nil {
			s.writeError(w, http.StatusBadRequest, s.text(r, "error.sendingOff"))
			return
		}
		var e mailer.Draft
		if err := readJSON(r, &e); err != nil {
			s.writeError(w, http.StatusBadRequest, s.text(r, "error.badJson"))
			return
		}
		var o mailer.Original
		if e.Key != "" {
			// On a reply, fetch the original's headers so the thread holds - and on a
			// forward the raw message for the attachment.
			obj, err := s.readMail(r, acc, e.Key, false)
			if err != nil {
				s.translate(w, r, err)
				return
			}
			o = mailer.Original{MessageID: obj.MessageID, References: obj.References,
				Subject: obj.Subject}
			if e.Mode == "forward" {
				if raw, err := acc.Mailbox.Fetch(r.Context(), e.Key, 0); err == nil {
					o.Raw = raw
				}
			}
		}
		n, err := mailer.Build(e, acc.From, o, time.Now())
		if err != nil {
			s.translate(w, r, err)
			return
		}
		id, err := acc.Sender.Send(r.Context(), n)
		if err != nil {
			s.translate(w, r, err)
			return
		}

		// From here on the message is out of the house. Nothing that follows may
		// turn the answer into an error - it would read as "not sent" and get
		// sent a second time.
		out := map[string]any{"message_id": id}
		if _, err := acc.Mailbox.Put(r.Context(), core.Sent, n.ID, n.Raw); err != nil {
			out["warning"] = s.text(r, "compose.sentNotStored")
		}
		if e.DraftKey != "" {
			if err := acc.Mailbox.DropDraft(r.Context(), e.DraftKey); err != nil {
				out["warning"] = s.text(r, "compose.draftNotRemoved")
			}
		}
		s.json(w, http.StatusOK, with(s.overview(r, acc), out))
	})
}

// draftRoute stores what somebody has written without sending it. The draft is
// a real message in the drafts folder - that way it survives the closed window,
// is visible from a second machine, and needs no storage of its own.
func (s *Server) draftRoute() {
	s.mux.HandleFunc("POST /api/draft", func(w http.ResponseWriter, r *http.Request) {
		acc, ok := s.pick(w, r)
		if !ok {
			return
		}
		var e mailer.Draft
		if err := readJSON(r, &e); err != nil {
			s.writeError(w, http.StatusBadRequest, s.text(r, "error.badJson"))
			return
		}
		n, err := mailer.BuildDraft(e, acc.From, mailer.Original{}, time.Now())
		if err != nil {
			s.translate(w, r, err)
			return
		}
		key, err := acc.Mailbox.Put(r.Context(), core.Drafts, n.ID, n.Raw)
		if err != nil {
			s.translate(w, r, err)
			return
		}
		// The previous version goes only once the new one lies there. The other
		// way round a failed write would leave nothing behind at all.
		if e.DraftKey != "" && e.DraftKey != key {
			_ = acc.Mailbox.DropDraft(r.Context(), e.DraftKey)
		}
		s.json(w, http.StatusOK, with(s.overview(r, acc), map[string]any{"key": key}))
	})
}

func (s *Server) wizardRoutes() {
	s.mux.HandleFunc("POST /api/setup/{aktion}", func(w http.ResponseWriter, r *http.Request) {
		if s.wizard == nil {
			s.writeError(w, http.StatusBadRequest, s.text(r, "error.noWizard"))
			return
		}
		fn, present := s.wizard.Route(r.URL.Path)
		if !present {
			s.writeError(w, http.StatusNotFound, s.text(r, "error.unknownAction"))
			return
		}
		var d wizard.Data
		if err := readJSON(r, &d); err != nil {
			s.writeError(w, http.StatusBadRequest, s.text(r, "error.badJson"))
			return
		}
		d.Language = s.language(r)
		res, err := fn(r.Context(), d)
		if err != nil {
			var input wizard.InputError
			if asInputError(err, &input) {
				s.writeError(w, http.StatusBadRequest, input.Text)
				return
			}
			s.translate(w, r, err)
			return
		}
		s.json(w, http.StatusOK, res)
	})
}
