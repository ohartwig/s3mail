package web

import (
	"context"
	"net/http"
	"strings"
)

// SuppressionList is the slice of the SES suppression list the server needs. An
// interface, so the tests get by without AWS.
type SuppressionList interface {
	Block(ctx context.Context, address string) error
	Unblock(ctx context.Context, address string) error
	List(ctx context.Context) ([]SuppressionEntry, error)
}

// SuppressionEntry is a blocked address.
type SuppressionEntry struct {
	Address string `json:"address"`
	Reason  string `json:"reason"`
	Since   string `json:"since"`
}

func (s *Server) suppressionRoutes() {
	s.mux.HandleFunc("GET /api/blocked", func(w http.ResponseWriter, r *http.Request) {
		acc, ok := s.pick(w, r)
		if !ok {
			return
		}
		if acc.Blocked == nil {
			s.writeError(w, http.StatusBadRequest, s.text(r, "error.noBlocklist"))
			return
		}
		list, err := acc.Blocked.List(r.Context())
		if err != nil {
			s.translate(w, r, err)
			return
		}
		s.json(w, http.StatusOK, map[string]any{"blocked": list})
	})

	s.post("/api/block", func(w http.ResponseWriter, r *http.Request, a request, acc *Account) {
		s.changeSuppression(w, r, acc, a, true)
	})
	s.post("/api/unblock", func(w http.ResponseWriter, r *http.Request, a request, acc *Account) {
		s.changeSuppression(w, r, acc, a, false)
	})
}

func (s *Server) changeSuppression(w http.ResponseWriter, r *http.Request, acc *Account,
	a request, block bool) {
	if acc.Blocked == nil {
		s.writeError(w, http.StatusBadRequest, s.text(r, "error.noBlocklist"))
		return
	}
	address := addressFrom(a.Address)
	if address == "" {
		s.writeError(w, http.StatusBadRequest, s.text(r, "error.noAddress"))
		return
	}
	var err error
	if block {
		err = acc.Blocked.Block(r.Context(), address)
	} else {
		err = acc.Blocked.Unblock(r.Context(), address)
	}
	if err != nil {
		s.translate(w, r, err)
		return
	}
	list, err := acc.Blocked.List(r.Context())
	if err != nil {
		// Entered is entered - that the list could not be read afterwards must not
		// make the action look like it failed.
		s.json(w, http.StatusOK, map[string]any{"address": address})
		return
	}
	s.json(w, http.StatusOK, map[string]any{"address": address, "blocked": list})
}

// addressFrom takes the bare address out of a header: `First Last <a@x.com>`
// becomes `a@x.com`. Without it the display name would land on the suppression
// list, and SES would refuse the entry.
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
