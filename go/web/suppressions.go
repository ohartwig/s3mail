package web

import (
	"context"
	"net/http"
	"strings"
)

// SuppressionList ist der Ausschnitt der SES-Unterdrueckungsliste, den der Server
// braucht. Als Interface, damit die Tests ohne AWS auskommen.
type SuppressionList interface {
	Block(ctx context.Context, address string) error
	Unblock(ctx context.Context, address string) error
	List(ctx context.Context) ([]SuppressionEntry, error)
}

// SuppressionEntry ist eine gesperrte Adresse.
type SuppressionEntry struct {
	Address string `json:"address"`
	Reason  string `json:"reason"`
	Since   string `json:"since"`
}

// WithSuppressionList schaltet die Routen dafuer frei.
func (s *Server) WithSuppressionList(l SuppressionList) { s.suppressions = l }

func (s *Server) suppressionRoutes() {
	s.mux.HandleFunc("GET /api/blocked", func(w http.ResponseWriter, r *http.Request) {
		if s.suppressions == nil {
			s.writeError(w, http.StatusBadRequest, s.text(r, "error.noBlocklist"))
			return
		}
		list, err := s.suppressions.List(r.Context())
		if err != nil {
			s.translate(w, err)
			return
		}
		s.json(w, http.StatusOK, map[string]any{"blocked": list})
	})

	s.post("/api/block", func(w http.ResponseWriter, r *http.Request, a request) {
		s.changeSuppression(w, r, a, true)
	})
	s.post("/api/unblock", func(w http.ResponseWriter, r *http.Request, a request) {
		s.changeSuppression(w, r, a, false)
	})
}

func (s *Server) changeSuppression(w http.ResponseWriter, r *http.Request, a request, block1 bool) {
	if s.suppressions == nil {
		s.writeError(w, http.StatusBadRequest, s.text(r, "error.noBlocklist"))
		return
	}
	address := addressFrom(a.Address)
	if address == "" {
		s.writeError(w, http.StatusBadRequest, s.text(r, "error.noAddress"))
		return
	}
	var err error
	if block1 {
		err = s.suppressions.Block(r.Context(), address)
	} else {
		err = s.suppressions.Unblock(r.Context(), address)
	}
	if err != nil {
		s.translate(w, err)
		return
	}
	list, err := s.suppressions.List(r.Context())
	if err != nil {
		// Eingetragen ist eingetragen - dass die Liste danach nicht zu lesen war,
		// darf die Handlung nicht als gescheitert erscheinen lassen.
		s.json(w, http.StatusOK, map[string]any{"address": address})
		return
	}
	s.json(w, http.StatusOK, map[string]any{"address": address, "blocked": list})
}

// addressFrom holt die nackte Adresse aus einer Kopfzeile: aus
// `Vorname Nachname <a@x.de>` wird `a@x.de`. Ohne das landete der Anzeigename
// auf der Sperrliste, und SES lehnte den Eintrag ab.
func addressFrom(s string) string {
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
