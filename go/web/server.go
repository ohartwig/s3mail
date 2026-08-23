package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"s3mail/assistent"
	"s3mail/core"
	"s3mail/i18n"
	"s3mail/mimeparse"
	"s3mail/store"
)

// Server ist der lokale Webserver. Er bindet an 127.0.0.1 und kennt keine
// Benutzer - der Zugang haengt an einem Token, siehe zugangPruefen.
type Server struct {
	Mailbox *store.Mailbox
	Token   string
	Bind    string
	Port    int
	Config  map[string]any

	versender        Versender
	sperrliste       Sperrliste
	standardAbsender string
	assistent        *assistent.Assistent

	// BeimBeenden wird von /api/quit gerufen. Ohne das laeuft der Server nach
	// dem Schliessen des Fensters weiter, und niemand sieht, dass er noch da ist.
	BeimBeenden func()

	mux *http.ServeMux
}

func NewServer(mb *store.Mailbox, token, bind string, port int, config map[string]any) *Server {
	s := &Server{Mailbox: mb, Token: token, Bind: bind, Port: port, Config: config}
	s.mux = http.NewServeMux()
	s.routen()
	s.sendenRoute()
	s.sperrlistenRouten()
	s.assistentRouten()
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if !s.zugangPruefen(w, r) {
		return
	}
	// Ohne Konfiguration gibt es noch kein Postfach - dann bedienen nur der
	// Assistent und /api/quit. Sonst liefen die Postfach-Routen in einen
	// Nil-Zeiger; und ohne die Ausnahme fuer quit koennte man ausgerechnet im
	// Assistenten nicht beenden, also in dem Zustand, in dem ein Erstnutzer steckt.
	if s.Mailbox == nil && strings.HasPrefix(r.URL.Path, "/api/") &&
		!strings.HasPrefix(r.URL.Path, "/api/setup/") && r.URL.Path != "/api/quit" {
		s.fehler(w, http.StatusServiceUnavailable, s.text(r, "error.notSetUp"))
		return
	}
	s.mux.ServeHTTP(w, r)
}

// -- Zugangskontrolle ------------------------------------------------------- //

var loopback = map[string]bool{"127.0.0.1": true, "localhost": true, "::1": true}

// hostOK schuetzt gegen DNS-Rebinding: eine fremde Domain, die auf 127.0.0.1
// zeigt, waere sonst dieselbe Herkunft wie s3mail und duerfte das Postfach
// auslesen. Der Browser schickt in dem Fall aber ihren Namen im Host-Header mit.
func (s *Server) hostOK(r *http.Request) bool {
	if !loopback[s.Bind] {
		return true // nach aussen gebunden: Auth macht der Reverse-Proxy
	}
	name, port := r.Host, ""
	if h, p, err := net.SplitHostPort(r.Host); err == nil {
		name, port = h, p
	}
	return loopback[name] && (port == "" || port == strconv.Itoa(s.Port))
}

// originOK schuetzt gegen CSRF: eine fremde Seite kann per fetch() einen POST
// hierher schicken (Content-Type text/plain, kein Preflight). Lesen kann sie die
// Antwort nicht, aber Loeschen und Versenden liefen trotzdem. Bei genau solchen
// Anfragen setzt der Browser die Origin.
func (s *Server) originOK(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" || o == "null" {
		return true
	}
	u, err := url.Parse(o)
	if err != nil {
		return false
	}
	return loopback[u.Hostname()] && (u.Port() == "" || u.Port() == strconv.Itoa(s.Port))
}

// token holt das Token aus Header, Query oder Cookie.
func (s *Server) token(r *http.Request) string {
	if t := r.Header.Get("X-S3mail-Token"); t != "" {
		return t
	}
	if t := r.URL.Query().Get("t"); t != "" {
		return t
	}
	if c, err := r.Cookie("s3mail"); err == nil {
		return c.Value
	}
	return ""
}

func (s *Server) zugangPruefen(w http.ResponseWriter, r *http.Request) bool {
	if !s.hostOK(r) {
		http.Error(w, "s3mail: unerwarteter Host-Header", http.StatusForbidden)
		return false
	}
	if !s.originOK(r) {
		s.fehler(w, http.StatusForbidden, "Anfrage von einer fremden Herkunft abgelehnt")
		return false
	}
	if !gleich(s.token(r), s.Token) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			s.fehler(w, http.StatusForbidden, s.text(r, "error.badToken"))
		} else {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(page("token", SeiteToken, s.language(r), nil)))
		}
		return false
	}
	return true
}

// -- Antworten -------------------------------------------------------------- //

func (s *Server) json(w http.ResponseWriter, code int, v any) {
	blob, err := json.Marshal(v)
	if err != nil {
		http.Error(w, "Antwort nicht darstellbar", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_, _ = w.Write(blob)
}

func (s *Server) fehler(w http.ResponseWriter, code int, text string) {
	s.json(w, code, map[string]string{"error": text})
}

// uebersetzen bildet Fehler aus der Logik auf Statuscodes ab - damit die Schichten
// darunter passende Fehler werfen koennen statt Statuscodes zu bauen.
func (s *Server) uebersetzen(w http.ResponseWriter, err error) {
	switch {
	case err == nil:
		return
	case errors.Is(err, store.ErrNurAusPapierkorb), errors.Is(err, store.ErrLoeschenGesperrt),
		errors.Is(err, store.ErrKeinKMS):
		s.fehler(w, http.StatusForbidden, err.Error())
	case errors.Is(err, store.ErrNichtGefunden):
		s.fehler(w, http.StatusNotFound, err.Error())
	case strings.Contains(err.Error(), "ungueltig"), strings.Contains(err.Error(), "ausserhalb"),
		strings.Contains(err.Error(), "interne Datei"):
		s.fehler(w, http.StatusBadRequest, err.Error())
	default:
		s.fehler(w, http.StatusBadGateway, err.Error())
	}
}

// seite liefert eine Seite aus und setzt dabei das Token als Cookie - dadurch
// funktionieren die Download-Links fuer Anhaenge und .eml, die keinen eigenen
// Header setzen koennen.
func (s *Server) seite(w http.ResponseWriter, inhalt string) {
	http.SetCookie(w, &http.Cookie{Name: "s3mail", Value: s.Token, Path: "/",
		HttpOnly: true, SameSite: http.SameSiteStrictMode})
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(inhalt))
}

// uebersicht haengt an den meisten Antworten mit dran, damit die Seitenleiste
// ohne zweiten Request aktuell ist.
//
// Die Ordner kommen mit ihrem Uebersetzungsschluessel aus core und werden hier
// zu Text - erst hier ist bekannt, welche Sprache der Fragende liest.
func (s *Server) uebersicht(r *http.Request) map[string]any {
	d := s.Mailbox.State.Data()
	return map[string]any{
		"folders":      s.ordnerMitBeschriftung(r),
		"tags":         d.Tags,
		"rules":        d.Rules,
		"state_remote": s.Mailbox.State.RemoteOK(),
		"allow_delete": s.Mailbox.AllowDelete,
	}
}

func mit(basis map[string]any, extra map[string]any) map[string]any {
	out := make(map[string]any, len(basis)+len(extra))
	for k, v := range basis {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

// -- Routen ----------------------------------------------------------------- //

type anfrage struct {
	Keys    []string    `json:"keys"`
	Key     string      `json:"key"`
	Folder  string      `json:"folder"`
	Read    *bool       `json:"read"`
	Star    *bool       `json:"star"`
	Add     []string    `json:"add"`
	Remove  []string    `json:"remove"`
	Action  string      `json:"action"`
	Name    string      `json:"name"`
	Old     string      `json:"old"`
	Color   string      `json:"color"`
	Rules   []core.Rule `json:"rules"`
	Address string      `json:"address"`
}

func (a anfrage) alleKeys() []string {
	if len(a.Keys) > 0 {
		return a.Keys
	}
	if a.Key != "" {
		return []string{a.Key}
	}
	return nil
}

func (s *Server) routen() {
	s.mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		if s.Mailbox == nil {
			s.seite(w, page("setup", SeiteAssistent, s.language(r), nil)) // noch nicht eingerichtet
			return
		}
		s.seite(w, page("inbox", SeitePostfach, s.language(r), s.Config))
	})
	s.mux.HandleFunc("GET /setup", func(w http.ResponseWriter, r *http.Request) {
		s.seite(w, page("setup", SeiteAssistent, s.language(r), nil))
	})

	// Picking a language is a GET that changes something, which is normally the
	// wrong shape - but the thing it changes is a cookie in this browser, not
	// data. A form post would need the token in a hidden field on every page.
	s.mux.HandleFunc("GET /language/{code}", func(w http.ResponseWriter, r *http.Request) {
		code := i18n.Get(r.PathValue("code")).Code
		http.SetCookie(w, &http.Cookie{Name: "s3mail_lang", Value: code, Path: "/",
			HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 60 * 60 * 24 * 365})
		ziel := r.Header.Get("Referer")
		if ziel == "" || !strings.HasPrefix(ziel, "http") {
			ziel = "/"
		}
		http.Redirect(w, r, ziel, http.StatusSeeOther)
	})

	s.mux.HandleFunc("GET /api/overview", func(w http.ResponseWriter, r *http.Request) {
		s.json(w, http.StatusOK, s.uebersicht(r))
	})

	s.mux.HandleFunc("GET /api/messages", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		o := core.SearchOpts{
			Tag:        q.Get("tag"),
			OnlyUnread: q.Get("unread") == "1",
			OnlyStar:   q.Get("star") == "1",
		}
		if f := q.Get("folder"); f != "*" {
			ordner, err := core.ValidFolder(f)
			if err != nil {
				s.fehler(w, http.StatusBadRequest, err.Error())
				return
			}
			o.Folder = &ordner
		}
		s.json(w, http.StatusOK, mit(s.uebersicht(r), map[string]any{
			"messages": s.Mailbox.Suche(q.Get("q"), o),
		}))
	})

	s.mux.HandleFunc("GET /api/message", func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Query().Get("key")
		voll, err := s.mailLesen(r, key, true)
		if err != nil {
			s.uebersetzen(w, err)
			return
		}
		e := s.Mailbox.State.Get(s.Mailbox.Mid(key))
		tags := e.Tags
		if tags == nil {
			tags = []string{}
		}
		s.json(w, http.StatusOK, mit(map[string]any{
			"key": key, "mid": s.Mailbox.Mid(key), "folder": s.Mailbox.FolderOf(key),
			"read": true, "star": e.Star, "tags": tags,
		}, alsMap(voll)))
	})

	s.mux.HandleFunc("GET /api/attachment", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		idx, _ := strconv.Atoi(q.Get("index"))
		voll, err := s.mailLesen(r, q.Get("key"), false)
		if err != nil {
			s.uebersetzen(w, err)
			return
		}
		for _, a := range voll.Anhaenge {
			if a.Index != idx {
				continue
			}
			ct := a.ContentType
			if ct == "" {
				ct = "application/octet-stream"
			}
			w.Header().Set("Content-Type", ct)
			w.Header().Set("Content-Disposition",
				fmt.Sprintf(`attachment; filename="%s"`, sauberName(a.Filename)))
			_, _ = w.Write(a.Inhalt)
			return
		}
		s.fehler(w, http.StatusNotFound, "Anhang nicht gefunden")
	})

	s.mux.HandleFunc("GET /api/raw", func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Query().Get("key")
		if err := s.Mailbox.Own(key); err != nil {
			s.uebersetzen(w, err)
			return
		}
		roh, err := s.Mailbox.Fetch(r.Context(), key, 0)
		if err != nil {
			s.uebersetzen(w, err)
			return
		}
		w.Header().Set("Content-Type", "message/rfc822")
		w.Header().Set("Content-Disposition",
			fmt.Sprintf(`attachment; filename="%s.eml"`, sauberName(s.Mailbox.Mid(key))))
		_, _ = w.Write(roh)
	})

	s.post("/api/refresh", func(w http.ResponseWriter, r *http.Request, a anfrage) {
		erg, err := s.Mailbox.Refresh(r.Context())
		if err != nil {
			s.uebersetzen(w, err)
			return
		}
		if _, err := s.Mailbox.ApplyRules(r.Context(), nil, false); err != nil {
			s.uebersetzen(w, err)
			return
		}
		s.json(w, http.StatusOK, mit(s.uebersicht(r), map[string]any{
			"geprueft": erg.Geprueft, "neu": erg.Neu, "entfernt": erg.Entfernt}))
	})

	s.post("/api/move", func(w http.ResponseWriter, r *http.Request, a anfrage) {
		erg, err := s.Mailbox.Move(r.Context(), a.alleKeys(), a.Folder)
		if err != nil {
			s.uebersetzen(w, err)
			return
		}
		s.json(w, http.StatusOK, mit(s.uebersicht(r), map[string]any{"moved": erg}))
	})

	s.post("/api/delete", func(w http.ResponseWriter, r *http.Request, a anfrage) {
		n, err := s.Mailbox.Delete(r.Context(), a.alleKeys(), false)
		if err != nil {
			s.uebersetzen(w, err)
			return
		}
		s.json(w, http.StatusOK, mit(s.uebersicht(r), map[string]any{"deleted": n}))
	})

	s.post("/api/empty-trash", func(w http.ResponseWriter, r *http.Request, a anfrage) {
		n, err := s.Mailbox.EmptyTrash(r.Context())
		if err != nil {
			s.uebersetzen(w, err)
			return
		}
		s.json(w, http.StatusOK, mit(s.uebersicht(r), map[string]any{"deleted": n}))
	})

	s.post("/api/flag", func(w http.ResponseWriter, r *http.Request, a anfrage) {
		s.mutieren(w, r, core.Op{T: "flags", Mids: s.mids(a), Read: a.Read, Star: a.Star})
	})

	s.post("/api/tag", func(w http.ResponseWriter, r *http.Request, a anfrage) {
		s.mutieren(w, r, core.Op{T: "tags", Mids: s.mids(a), Add: a.Add, Remove: a.Remove})
	})

	s.post("/api/tags", func(w http.ResponseWriter, r *http.Request, a anfrage) {
		switch a.Action {
		case "delete":
			s.mutieren(w, r, core.Op{T: "tagdel", Name: a.Name})
		case "rename", "create":
			alt := a.Old
			if alt == "" {
				alt = a.Name
			}
			s.mutieren(w, r, core.Op{T: "tagren", Old: alt, New: a.Name, Color: a.Color})
		default:
			s.fehler(w, http.StatusBadRequest, s.text(r, "error.unknownTagAction"))
		}
	})

	s.post("/api/rules", func(w http.ResponseWriter, r *http.Request, a anfrage) {
		sauber, err := core.CleanRules(a.Rules)
		if err != nil {
			s.fehler(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := s.Mailbox.State.Mutate(r.Context(), core.Op{T: "rules", Rules: sauber}); err != nil {
			s.uebersetzen(w, err)
			return
		}
		s.json(w, http.StatusOK, mit(s.uebersicht(r), map[string]any{"rules": sauber}))
	})

	s.post("/api/quit", func(w http.ResponseWriter, r *http.Request, a anfrage) {
		s.json(w, http.StatusOK, map[string]any{"ok": true})
		if s.BeimBeenden != nil {
			// Erst antworten, dann herunterfahren - sonst sieht die Oberflaeche
			// einen Verbindungsabbruch statt einer Bestaetigung.
			go func() {
				time.Sleep(150 * time.Millisecond)
				s.BeimBeenden()
			}()
		}
	})

	s.post("/api/rules/apply", func(w http.ResponseWriter, r *http.Request, a anfrage) {
		n, err := s.Mailbox.ApplyRules(r.Context(), nil, true)
		if err != nil {
			s.uebersetzen(w, err)
			return
		}
		s.json(w, http.StatusOK, mit(s.uebersicht(r), map[string]any{"moved": n}))
	})
}

func (s *Server) post(pfad string, fn func(http.ResponseWriter, *http.Request, anfrage)) {
	s.mux.HandleFunc("POST "+pfad, func(w http.ResponseWriter, r *http.Request) {
		var a anfrage
		if r.ContentLength != 0 {
			if err := json.NewDecoder(r.Body).Decode(&a); err != nil {
				s.fehler(w, http.StatusBadRequest, s.text(r, "error.badJson"))
				return
			}
		}
		fn(w, r, a)
	})
}

func (s *Server) mids(a anfrage) []string {
	keys := a.alleKeys()
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, s.Mailbox.Mid(k))
	}
	return out
}

func (s *Server) mutieren(w http.ResponseWriter, r *http.Request, op core.Op) {
	if err := s.Mailbox.State.Mutate(r.Context(), op); err != nil {
		s.uebersetzen(w, err)
		return
	}
	s.json(w, http.StatusOK, mit(s.uebersicht(r), map[string]any{"ok": true}))
}

// mailLesen holt eine Mail und markiert sie auf Wunsch als gelesen.
func (s *Server) mailLesen(r *http.Request, key string, alsGelesen bool) (mimeparse.Voll, error) {
	if err := s.Mailbox.Own(key); err != nil {
		return mimeparse.Voll{}, err
	}
	roh, err := s.Mailbox.Fetch(r.Context(), key, 0)
	if err != nil {
		return mimeparse.Voll{}, err
	}
	voll := mimeparse.Lesen(roh, time.Unix(0, 0).UTC())
	if alsGelesen {
		_ = s.Mailbox.State.Mutate(r.Context(), core.Op{T: "flags",
			Mids: []string{s.Mailbox.Mid(key)}, Read: core.Ptr(true)})
	}
	return voll, nil
}

func alsMap(v mimeparse.Voll) map[string]any {
	blob, _ := json.Marshal(v)
	var m map[string]any
	_ = json.Unmarshal(blob, &m)
	return m
}

var unsauber = regexp.MustCompile(`[^\w.\- ]`)

func sauberName(s string) string { return unsauber.ReplaceAllString(s, "_") }

// ordnerMitBeschriftung ersetzt den Uebersetzungsschluessel der Systemordner
// durch Text. Selbst angelegte Ordner tragen ihren Namen und bleiben, wie sie
// sind - sie hat jemand so genannt.
func (s *Server) ordnerMitBeschriftung(r *http.Request) []core.FolderInfo {
	cat := i18n.Get(s.language(r))
	ordner := s.Mailbox.Ordner()
	for i, f := range ordner {
		if f.System {
			ordner[i].Label = cat.T(f.Label)
		}
	}
	return ordner
}

// text holt einen Satz in der Sprache der Anfrage. Fuer die Handvoll Meldungen,
// die der Server selbst erzeugt - alles andere kommt aus den Seiten.
func (s *Server) text(r *http.Request, key string) string {
	return i18n.Get(s.language(r)).T(key)
}
