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

	"s3mail/config"
	"s3mail/core"
	"s3mail/i18n"
	"s3mail/mailer"
	"s3mail/mimeparse"
	"s3mail/store"
	"s3mail/wizard"
)

// Server is the local web server. It binds to 127.0.0.1 and knows no users - the
// access hangs on a token, see guard.
type Server struct {
	Mailbox *store.Mailbox
	Token   string
	Bind    string
	Port    int
	Config  map[string]any

	sender       Sender
	suppressions SuppressionList
	defaultFrom  string
	wizard       *wizard.Wizard

	// OnShutdown is called by /api/quit. Without it the server keeps running after
	// the window is closed, and nobody sees that it is still there.
	OnShutdown func()

	mux *http.ServeMux
}

func NewServer(mb *store.Mailbox, token, bind string, port int, config map[string]any) *Server {
	s := &Server{Mailbox: mb, Token: token, Bind: bind, Port: port, Config: config}
	s.mux = http.NewServeMux()
	s.routes()
	s.sendRoute()
	s.suppressionRoutes()
	s.wizardRoutes()
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if !s.checkAccess(w, r) {
		return
	}
	// Without a configuration there is no mailbox yet - then only the wizard and
	// /api/quit answer. Otherwise the mailbox routes would run into a nil pointer;
	// and without the exception for quit, quitting would be impossible in the
	// wizard of all places, the state a first-time reader is in.
	if s.Mailbox == nil && strings.HasPrefix(r.URL.Path, "/api/") &&
		!strings.HasPrefix(r.URL.Path, "/api/setup/") && r.URL.Path != "/api/quit" {
		s.writeError(w, http.StatusServiceUnavailable, s.text(r, "error.notSetUp"))
		return
	}
	s.mux.ServeHTTP(w, r)
}

// -- Zugangskontrolle ------------------------------------------------------- //

var loopback = map[string]bool{"127.0.0.1": true, "localhost": true, "::1": true}

// hostOK guards against DNS rebinding: a foreign domain pointing at 127.0.0.1
// would otherwise be the same origin as s3mail and could read the mailbox out.
// In that case, though, the browser sends its name in the Host header.
func (s *Server) hostOK(r *http.Request) bool {
	if !loopback[s.Bind] {
		return true // bound outwards: authentication is the reverse proxy's job
	}
	name, port := r.Host, ""
	if h, p, err := net.SplitHostPort(r.Host); err == nil {
		name, port = h, p
	}
	return loopback[name] && (port == "" || port == strconv.Itoa(s.Port))
}

// originOK guards against CSRF: a foreign page can send a POST here via fetch()
// (content type text/plain, no preflight). It cannot read the answer, but
// deleting and sending would happen all the same. On exactly those requests the
// browser sets the Origin.
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

// token takes the token from header, query or cookie.
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

func (s *Server) checkAccess(w http.ResponseWriter, r *http.Request) bool {
	if !s.hostOK(r) {
		http.Error(w, s.text(r, "error.badHost"), http.StatusForbidden)
		return false
	}
	if !s.originOK(r) {
		s.writeError(w, http.StatusForbidden, s.text(r, "error.foreignOrigin"))
		return false
	}
	if !equal(s.token(r), s.Token) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			s.writeError(w, http.StatusForbidden, s.text(r, "error.badToken"))
		} else {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(page("token", PageToken, s.language(r), nil)))
		}
		return false
	}
	return true
}

// -- Antworten -------------------------------------------------------------- //

func (s *Server) json(w http.ResponseWriter, code int, v any) {
	blob, err := json.Marshal(v)
	if err != nil {
		http.Error(w, "answer cannot be rendered", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_, _ = w.Write(blob)
}

func (s *Server) writeError(w http.ResponseWriter, code int, text string) {
	s.json(w, code, map[string]string{"error": text})
}

// translate maps errors from the logic onto status codes - so the layers below
// throw exceptions instead of building status codes.
func (s *Server) translate(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case err == nil:
		return
	case errors.Is(err, store.ErrTrashOnly):
		s.writeError(w, http.StatusForbidden, s.text(r, "error.trashOnly"))
	case errors.Is(err, store.ErrDeleteBlocked):
		s.writeError(w, http.StatusForbidden, s.text(r, "error.deleteBlocked"))
	case errors.Is(err, store.ErrNoKMS):
		s.writeError(w, http.StatusForbidden, s.text(r, "error.noKms"))
	case errors.Is(err, store.ErrNotFound):
		s.writeError(w, http.StatusNotFound, s.text(r, "error.notFound"))
	case errors.Is(err, core.ErrBadInput):
		s.writeError(w, http.StatusBadRequest, s.text(r, "error.badInput"))
	case errors.Is(err, mailer.ErrNoSender):
		s.writeError(w, http.StatusBadRequest, s.text(r, "error.noSender"))
	case errors.Is(err, mailer.ErrNoRecipient):
		s.writeError(w, http.StatusBadRequest, s.text(r, "error.noRecipient"))
	case errors.Is(err, config.ErrNoBucket):
		s.writeError(w, http.StatusBadRequest, s.text(r, "error.noBucket"))
	case errors.Is(err, config.ErrCredentialsIncomplete):
		s.writeError(w, http.StatusBadRequest, s.text(r, "error.credentialsIncomplete"))
	case errors.Is(err, config.ErrBadKeyID):
		s.writeError(w, http.StatusBadRequest, s.text(r, "error.badKeyId"))
	default:
		// Everything else is a diagnosis, not a sentence for the reader: the SDK
		// message goes through unchanged, because it is the only thing that says
		// what actually happened.
		s.writeError(w, http.StatusBadGateway, err.Error())
	}
}

// writePage serves a page and sets the token as a cookie while doing so - which
// is what makes the download links for attachments and .eml work without a token
// of their own.
func (s *Server) writePage(w http.ResponseWriter, content string) {
	http.SetCookie(w, &http.Cookie{Name: "s3mail", Value: s.Token, Path: "/",
		HttpOnly: true, SameSite: http.SameSiteStrictMode})
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(content))
}

// overview rides along on most answers, so the sidebar is current without a
// second request.
//
// The folders come from core with their translation key and become text here -
// only here is it known which language the asker reads.
func (s *Server) overview(r *http.Request) map[string]any {
	d := s.Mailbox.State.Data()
	return map[string]any{
		"folders":      s.localizedFolders(r),
		"tags":         d.Tags,
		"rules":        d.Rules,
		"state_remote": s.Mailbox.State.RemoteOK(),
		"allow_delete": s.Mailbox.AllowDelete,
	}
}

func with(base map[string]any, extra map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(extra))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

// -- Routen ----------------------------------------------------------------- //

type request struct {
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

func (a request) allKeys() []string {
	if len(a.Keys) > 0 {
		return a.Keys
	}
	if a.Key != "" {
		return []string{a.Key}
	}
	return nil
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		if s.Mailbox == nil {
			s.writePage(w, page("setup", PageWizard, s.language(r), nil)) // not set up yet
			return
		}
		s.writePage(w, page("inbox", PageMailbox, s.language(r), s.Config))
	})
	s.mux.HandleFunc("GET /setup", func(w http.ResponseWriter, r *http.Request) {
		s.writePage(w, page("setup", PageWizard, s.language(r), nil))
	})

	// Picking a language is a GET that changes something, which is normally the
	// wrong shape - but the thing it changes is a cookie in this browser, not
	// data. A form post would need the token in a hidden field on every page.
	s.mux.HandleFunc("GET /language/{code}", func(w http.ResponseWriter, r *http.Request) {
		code := i18n.Get(r.PathValue("code")).Code
		http.SetCookie(w, &http.Cookie{Name: "s3mail_lang", Value: code, Path: "/",
			HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 60 * 60 * 24 * 365})
		target := r.Header.Get("Referer")
		if target == "" || !strings.HasPrefix(target, "http") {
			target = "/"
		}
		http.Redirect(w, r, target, http.StatusSeeOther)
	})

	s.mux.HandleFunc("GET /api/overview", func(w http.ResponseWriter, r *http.Request) {
		s.json(w, http.StatusOK, s.overview(r))
	})

	s.mux.HandleFunc("GET /api/messages", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		o := core.SearchOpts{
			Tag:        q.Get("tag"),
			OnlyUnread: q.Get("unread") == "1",
			OnlyStar:   q.Get("star") == "1",
		}
		if f := q.Get("folder"); f != "*" {
			folder, err := core.ValidFolder(f)
			if err != nil {
				s.writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			o.Folder = &folder
		}
		s.json(w, http.StatusOK, with(s.overview(r), map[string]any{
			"messages": s.localizedSubjects(r, s.Mailbox.Search(q.Get("q"), o)),
		}))
	})

	s.mux.HandleFunc("GET /api/message", func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Query().Get("key")
		obj, err := s.readMail(r, key, true)
		if err != nil {
			s.translate(w, r, err)
			return
		}
		switch obj.Subject {
		case mimeparse.SubjectUnreadable:
			obj.Subject = s.text(r, "mail.unreadable")
		case mimeparse.SubjectNone:
			obj.Subject = s.text(r, "mail.noSubject")
		}
		e := s.Mailbox.State.Get(s.Mailbox.Mid(key))
		tags := e.Tags
		if tags == nil {
			tags = []string{}
		}
		s.json(w, http.StatusOK, with(map[string]any{
			"key": key, "mid": s.Mailbox.Mid(key), "folder": s.Mailbox.FolderOf(key),
			"read": true, "star": e.Star, "tags": tags,
		}, alsMap(obj)))
	})

	s.mux.HandleFunc("GET /api/attachment", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		idx, _ := strconv.Atoi(q.Get("index"))
		obj, err := s.readMail(r, q.Get("key"), false)
		if err != nil {
			s.translate(w, r, err)
			return
		}
		for _, a := range obj.Attachments {
			if a.Index != idx {
				continue
			}
			ct := a.ContentType
			if ct == "" {
				ct = "application/octet-stream"
			}
			w.Header().Set("Content-Type", ct)
			w.Header().Set("Content-Disposition",
				fmt.Sprintf(`attachment; filename="%s"`, cleanName(a.Filename)))
			_, _ = w.Write(a.Content)
			return
		}
		s.writeError(w, http.StatusNotFound, s.text(r, "error.attachmentNotFound"))
	})

	s.mux.HandleFunc("GET /api/raw", func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Query().Get("key")
		if err := s.Mailbox.Own(key); err != nil {
			s.translate(w, r, err)
			return
		}
		raw, err := s.Mailbox.Fetch(r.Context(), key, 0)
		if err != nil {
			s.translate(w, r, err)
			return
		}
		w.Header().Set("Content-Type", "message/rfc822")
		w.Header().Set("Content-Disposition",
			fmt.Sprintf(`attachment; filename="%s.eml"`, cleanName(s.Mailbox.Mid(key))))
		_, _ = w.Write(raw)
	})

	s.post("/api/refresh", func(w http.ResponseWriter, r *http.Request, a request) {
		res, err := s.Mailbox.Refresh(r.Context())
		if err != nil {
			s.translate(w, r, err)
			return
		}
		if _, err := s.Mailbox.ApplyRules(r.Context(), nil, false); err != nil {
			s.translate(w, r, err)
			return
		}
		s.json(w, http.StatusOK, with(s.overview(r), map[string]any{
			"geprueft": res.Checked, "neu": res.New, "entfernt": res.Removed}))
	})

	s.post("/api/move", func(w http.ResponseWriter, r *http.Request, a request) {
		res, err := s.Mailbox.Move(r.Context(), a.allKeys(), a.Folder)
		if err != nil {
			s.translate(w, r, err)
			return
		}
		s.json(w, http.StatusOK, with(s.overview(r), map[string]any{"moved": res}))
	})

	s.post("/api/delete", func(w http.ResponseWriter, r *http.Request, a request) {
		n, err := s.Mailbox.Delete(r.Context(), a.allKeys(), false)
		if err != nil {
			s.translate(w, r, err)
			return
		}
		s.json(w, http.StatusOK, with(s.overview(r), map[string]any{"deleted": n}))
	})

	s.post("/api/empty-trash", func(w http.ResponseWriter, r *http.Request, a request) {
		n, err := s.Mailbox.EmptyTrash(r.Context())
		if err != nil {
			s.translate(w, r, err)
			return
		}
		s.json(w, http.StatusOK, with(s.overview(r), map[string]any{"deleted": n}))
	})

	s.post("/api/flag", func(w http.ResponseWriter, r *http.Request, a request) {
		s.mutate(w, r, core.Op{T: "flags", Mids: s.midsOf(a), Read: a.Read, Star: a.Star})
	})

	s.post("/api/tag", func(w http.ResponseWriter, r *http.Request, a request) {
		s.mutate(w, r, core.Op{T: "tags", Mids: s.midsOf(a), Add: a.Add, Remove: a.Remove})
	})

	s.post("/api/tags", func(w http.ResponseWriter, r *http.Request, a request) {
		switch a.Action {
		case "delete":
			s.mutate(w, r, core.Op{T: "tagdel", Name: a.Name})
		case "rename", "create":
			old := a.Old
			if old == "" {
				old = a.Name
			}
			s.mutate(w, r, core.Op{T: "tagren", Old: old, New: a.Name, Color: a.Color})
		default:
			s.writeError(w, http.StatusBadRequest, s.text(r, "error.unknownTagAction"))
		}
	})

	s.post("/api/rules", func(w http.ResponseWriter, r *http.Request, a request) {
		clean, err := core.CleanRules(a.Rules)
		if err != nil {
			s.writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := s.Mailbox.State.Mutate(r.Context(), core.Op{T: "rules", Rules: clean}); err != nil {
			s.translate(w, r, err)
			return
		}
		s.json(w, http.StatusOK, with(s.overview(r), map[string]any{"rules": clean}))
	})

	s.post("/api/quit", func(w http.ResponseWriter, r *http.Request, a request) {
		s.json(w, http.StatusOK, map[string]any{"ok": true})
		if s.OnShutdown != nil {
			// Answer first, shut down after - otherwise the interface sees a dropped
			// connection instead of a confirmation.
			go func() {
				time.Sleep(150 * time.Millisecond)
				s.OnShutdown()
			}()
		}
	})

	s.post("/api/rules/apply", func(w http.ResponseWriter, r *http.Request, a request) {
		n, err := s.Mailbox.ApplyRules(r.Context(), nil, true)
		if err != nil {
			s.translate(w, r, err)
			return
		}
		s.json(w, http.StatusOK, with(s.overview(r), map[string]any{"moved": n}))
	})
}

func (s *Server) post(path string, fn func(http.ResponseWriter, *http.Request, request)) {
	s.mux.HandleFunc("POST "+path, func(w http.ResponseWriter, r *http.Request) {
		var a request
		if r.ContentLength != 0 {
			if err := json.NewDecoder(r.Body).Decode(&a); err != nil {
				s.writeError(w, http.StatusBadRequest, s.text(r, "error.badJson"))
				return
			}
		}
		fn(w, r, a)
	})
}

func (s *Server) midsOf(a request) []string {
	keys := a.allKeys()
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, s.Mailbox.Mid(k))
	}
	return out
}

func (s *Server) mutate(w http.ResponseWriter, r *http.Request, op core.Op) {
	if err := s.Mailbox.State.Mutate(r.Context(), op); err != nil {
		s.translate(w, r, err)
		return
	}
	s.json(w, http.StatusOK, with(s.overview(r), map[string]any{"ok": true}))
}

// readMail fetches a message and marks it read on request.
func (s *Server) readMail(r *http.Request, key string, asRead bool) (mimeparse.Full, error) {
	if err := s.Mailbox.Own(key); err != nil {
		return mimeparse.Full{}, err
	}
	raw, err := s.Mailbox.Fetch(r.Context(), key, 0)
	if err != nil {
		return mimeparse.Full{}, err
	}
	obj := mimeparse.Read(raw, time.Unix(0, 0).UTC())
	if asRead {
		_ = s.Mailbox.State.Mutate(r.Context(), core.Op{T: "flags",
			Mids: []string{s.Mailbox.Mid(key)}, Read: core.Ptr(true)})
	}
	return obj, nil
}

func alsMap(v mimeparse.Full) map[string]any {
	blob, _ := json.Marshal(v)
	var m map[string]any
	_ = json.Unmarshal(blob, &m)
	return m
}

var unsafeChars = regexp.MustCompile(`[^\w.\- ]`)

func cleanName(s string) string { return unsafeChars.ReplaceAllString(s, "_") }

// localizedFolders replaces the translation key of the system folders with text.
// Folders somebody created carry their own name and stay as they are - somebody
// called them that.
func (s *Server) localizedFolders(r *http.Request) []core.FolderInfo {
	cat := i18n.Get(s.language(r))
	folder := s.Mailbox.Folders()
	for i, f := range folder {
		if f.System {
			folder[i].Label = cat.T(f.Label)
		}
	}
	return folder
}

// localizedSubjects swaps the two subject placeholders for the reader's
// language. They are written while indexing, and the index outlives every
// language switch - so the swap has to happen here, not there.
func (s *Server) localizedSubjects(r *http.Request, msgs []core.Message) []core.Message {
	for i := range msgs {
		switch msgs[i].Subject {
		case mimeparse.SubjectUnreadable:
			msgs[i].Subject = s.text(r, "mail.unreadable")
		case mimeparse.SubjectNone:
			msgs[i].Subject = s.text(r, "mail.noSubject")
		}
	}
	return msgs
}

// text takes a sentence in the language of the request. For the handful of
// messages the server produces itself - everything else comes from the pages.
func (s *Server) text(r *http.Request, key string) string {
	return i18n.Get(s.language(r)).T(key)
}
