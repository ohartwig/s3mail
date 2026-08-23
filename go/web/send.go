package web

import (
	"context"
	"net/http"
	"time"

	"s3mail/mailer"
	"s3mail/wizard"
)

// Sender is the slice of SES the server needs.
type Sender interface {
	Send(ctx context.Context, n mailer.Message) (string, error)
}

// WithSender enables replying and forwarding. Without it s3mail runs read-only.
// reinen Lesemodus (--no-send).
func (s *Server) WithSender(v Sender, defaultFrom string) {
	s.sender, s.defaultFrom = v, defaultFrom
}

// WithWizard attaches the /api/setup/* routes.
func (s *Server) WithWizard(a *wizard.Wizard) { s.wizard = a }

func (s *Server) sendRoute() {
	s.mux.HandleFunc("POST /api/send", func(w http.ResponseWriter, r *http.Request) {
		if s.sender == nil {
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
			obj, err := s.readMail(r, e.Key, false)
			if err != nil {
				s.translate(w, r, err)
				return
			}
			o = mailer.Original{MessageID: obj.MessageID, References: obj.References,
				Subject: obj.Subject}
			if e.Mode == "forward" {
				if raw, err := s.Mailbox.Fetch(r.Context(), e.Key, 0); err == nil {
					o.Raw = raw
				}
			}
		}
		n, err := mailer.Build(e, s.defaultFrom, o, time.Now())
		if err != nil {
			s.writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		id, err := s.sender.Send(r.Context(), n)
		if err != nil {
			s.translate(w, r, err)
			return
		}
		s.json(w, http.StatusOK, map[string]any{"message_id": id})
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
