package web

import (
	"context"
	"net/http"
	"strings"
)

// Sperrliste ist der Ausschnitt der SES-Unterdrueckungsliste, den der Server
// braucht. Als Interface, damit die Tests ohne AWS auskommen.
type Sperrliste interface {
	Sperren(ctx context.Context, adresse string) error
	Freigeben(ctx context.Context, adresse string) error
	Lesen(ctx context.Context) ([]Sperreintrag, error)
}

// Sperreintrag ist eine gesperrte Adresse.
type Sperreintrag struct {
	Adresse string `json:"address"`
	Grund   string `json:"reason"`
	Seit    string `json:"since"`
}

// MitSperrliste schaltet die Routen dafuer frei.
func (s *Server) MitSperrliste(l Sperrliste) { s.sperrliste = l }

func (s *Server) sperrlistenRouten() {
	s.mux.HandleFunc("GET /api/blocked", func(w http.ResponseWriter, r *http.Request) {
		if s.sperrliste == nil {
			s.writeError(w, http.StatusBadRequest, s.text(r, "error.noBlocklist"))
			return
		}
		liste, err := s.sperrliste.Lesen(r.Context())
		if err != nil {
			s.uebersetzen(w, err)
			return
		}
		s.json(w, http.StatusOK, map[string]any{"blocked": liste})
	})

	s.post("/api/block", func(w http.ResponseWriter, r *http.Request, a request) {
		s.sperrlisteAendern(w, r, a, true)
	})
	s.post("/api/unblock", func(w http.ResponseWriter, r *http.Request, a request) {
		s.sperrlisteAendern(w, r, a, false)
	})
}

func (s *Server) sperrlisteAendern(w http.ResponseWriter, r *http.Request, a request, sperren bool) {
	if s.sperrliste == nil {
		s.writeError(w, http.StatusBadRequest, s.text(r, "error.noBlocklist"))
		return
	}
	adresse := adresseAus(a.Address)
	if adresse == "" {
		s.writeError(w, http.StatusBadRequest, s.text(r, "error.noAddress"))
		return
	}
	var err error
	if sperren {
		err = s.sperrliste.Sperren(r.Context(), adresse)
	} else {
		err = s.sperrliste.Freigeben(r.Context(), adresse)
	}
	if err != nil {
		s.uebersetzen(w, err)
		return
	}
	liste, err := s.sperrliste.Lesen(r.Context())
	if err != nil {
		// Eingetragen ist eingetragen - dass die Liste danach nicht zu lesen war,
		// darf die Handlung nicht als gescheitert erscheinen lassen.
		s.json(w, http.StatusOK, map[string]any{"address": adresse})
		return
	}
	s.json(w, http.StatusOK, map[string]any{"address": adresse, "blocked": liste})
}

// adresseAus holt die nackte Adresse aus einer Kopfzeile: aus
// `Vorname Nachname <a@x.de>` wird `a@x.de`. Ohne das landete der Anzeigename
// auf der Sperrliste, und SES lehnte den Eintrag ab.
func adresseAus(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndex(s, "<"); i >= 0 {
		if j := strings.Index(s[i:], ">"); j > 0 {
			s = s[i+1 : i+j]
		}
	}
	s = strings.Trim(strings.TrimSpace(s), `"'`)
	if !strings.Contains(s, "@") || strings.ContainsAny(s, " ,;") {
		return ""
	}
	return s
}
