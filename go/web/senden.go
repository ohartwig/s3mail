package web

import (
	"context"
	"net/http"
	"time"

	"s3mail/assistent"
	"s3mail/mailer"
)

// Versender ist der Ausschnitt von SES, den der Server braucht.
type Versender interface {
	Senden(ctx context.Context, n mailer.Nachricht) (string, error)
}

// MitVersand schaltet Antworten und Weiterleiten frei. Ohne das laeuft s3mail im
// reinen Lesemodus (--no-send).
func (s *Server) MitVersand(v Versender, standardAbsender string) {
	s.versender, s.standardAbsender = v, standardAbsender
}

// MitAssistent haengt die /api/setup/*-Routen an.
func (s *Server) MitAssistent(a *assistent.Assistent) { s.assistent = a }

func (s *Server) sendenRoute() {
	s.mux.HandleFunc("POST /api/send", func(w http.ResponseWriter, r *http.Request) {
		if s.versender == nil {
			s.fehler(w, http.StatusBadRequest, "SES-Versand ist deaktiviert (--no-send)")
			return
		}
		var e mailer.Entwurf
		if err := jsonLesen(r, &e); err != nil {
			s.fehler(w, http.StatusBadRequest, "ungueltiges JSON")
			return
		}
		var o mailer.Original
		if e.Key != "" {
			// Beim Antworten die Kopfzeilen des Originals holen, damit der Faden
			// haelt - und beim Weiterleiten die Rohmail fuer den Anhang.
			voll, err := s.mailLesen(r, e.Key, false)
			if err != nil {
				s.uebersetzen(w, err)
				return
			}
			o = mailer.Original{MessageID: voll.MessageID, References: voll.References,
				Subject: voll.Subject}
			if e.Mode == "forward" {
				if roh, err := s.Mailbox.Fetch(r.Context(), e.Key, 0); err == nil {
					o.Roh = roh
				}
			}
		}
		n, err := mailer.Bauen(e, s.standardAbsender, o, time.Now())
		if err != nil {
			s.fehler(w, http.StatusBadRequest, err.Error())
			return
		}
		id, err := s.versender.Senden(r.Context(), n)
		if err != nil {
			s.uebersetzen(w, err)
			return
		}
		s.json(w, http.StatusOK, map[string]any{"message_id": id})
	})
}

func (s *Server) assistentRouten() {
	s.mux.HandleFunc("POST /api/setup/{aktion}", func(w http.ResponseWriter, r *http.Request) {
		if s.assistent == nil {
			s.fehler(w, http.StatusBadRequest, "Assistent ist nicht verfügbar")
			return
		}
		fn, da := s.assistent.Route(r.URL.Path)
		if !da {
			s.fehler(w, http.StatusNotFound, "unbekannte Aktion")
			return
		}
		var d assistent.Daten
		if err := jsonLesen(r, &d); err != nil {
			s.fehler(w, http.StatusBadRequest, "ungueltiges JSON")
			return
		}
		erg, err := fn(r.Context(), d)
		if err != nil {
			var eingabe assistent.Eingabefehler
			if asEingabefehler(err, &eingabe) {
				s.fehler(w, http.StatusBadRequest, eingabe.Text)
				return
			}
			s.uebersetzen(w, err)
			return
		}
		s.json(w, http.StatusOK, erg)
	})
}
