#!/usr/bin/env python3
"""s3mail - Weboberflaeche und CLI (nutzt s3mail_core)."""

from __future__ import annotations

import argparse
import json
import os
import re
import secrets
import sys
import threading
import traceback
import webbrowser
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, urlparse

from s3mail_core import *  # noqa: F401,F403
from s3mail_core import (INBOX, SPAM, TRASH, ARCHIVE, MailStore, Sender, ClientError)
from s3mail_setup import *  # noqa: F401,F403
from s3mail_setup import (CONFIG_FILE, SETUP_PAGE, load_config, save_config,
                          make_session, normalize_prefix, setup_api)

# === BUILD:SPLIT ===


# --------------------------------------------------------------------------- #
# HTTP-Server
# --------------------------------------------------------------------------- #
def activate(cfg: dict, no_send: bool = False) -> MailStore:
    """Aus einer Konfiguration den Store bauen und den Server scharf schalten."""
    session = make_session(cfg.get("profile"), cfg.get("region"))
    try:
        kms = session.client("kms")      # nur noetig, wenn SES client-seitig verschluesselt
    except Exception:
        kms = None
    store = MailStore(session.client("s3"), cfg["bucket"], cfg.get("prefix", ""),
                      allow_delete=bool(cfg.get("allow_delete", True)), kms=kms)
    sender = None if no_send else Sender(session.client("ses"), cfg.get("from") or None)
    Handler.store = store
    Handler.sender = sender
    Handler.config = {
        "bucket": cfg["bucket"], "root": store.root,
        "default_from": cfg.get("from") or "", "can_send": sender is not None,
        "config_file": CONFIG_FILE,
    }
    return store


class Handler(BaseHTTPRequestHandler):
    server_version = "s3mail/3.0"
    store: MailStore | None = None
    sender: Sender | None = None
    config: dict = {}
    no_send: bool = False
    token: str = ""              # Sitzungs-Token, siehe _guard_request()
    bind: str = "127.0.0.1"      # Adresse, an die der Server gebunden ist
    port: int = 8765

    LOOPBACK = ("127.0.0.1", "localhost", "::1")

    def log_message(self, fmt, *args):
        pass  # ruhiges Terminal; Fehler kommen ueber Tracebacks

    def _send(self, code: int, body: bytes, ctype: str, extra: dict | None = None):
        self.send_response(code)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(body)))
        self.send_header("X-Content-Type-Options", "nosniff")
        for k, v in (extra or {}).items():
            self.send_header(k, v)
        self.end_headers()
        if self.command != "HEAD":
            self.wfile.write(body)

    def _json(self, payload, code: int = 200):
        self._send(code, json.dumps(payload).encode("utf-8"),
                   "application/json; charset=utf-8")

    # -- gemeinsame Fehlerbehandlung ---------------------------------------- #
    def _guard(self, fn):
        try:
            fn()
        except PermissionError as exc:
            self._json({"error": str(exc)}, 403)
        except ValueError as exc:
            self._json({"error": str(exc)}, 400)
        except KeyError as exc:
            self._json({"error": f"Parameter/Objekt fehlt: {exc}"}, 400)
        except ClientError as exc:
            self._json({"error": str(exc)}, 502)
        except Exception as exc:
            traceback.print_exc()
            self._json({"error": f"{type(exc).__name__}: {exc}"}, 500)

    def _overview(self) -> dict:
        st = self.store.state
        return {
            "folders": self.store.folders(),
            "tags": st.data.get("tags", {}),
            "rules": st.data.get("rules", []),
            "status": self.store.status,
            "state_remote": st.remote_ok,
            "allow_delete": self.store.allow_delete,
        }

    # -- Zugang -------------------------------------------------------------- #
    def _host_ok(self) -> bool:
        """Gegen DNS-Rebinding: eine fremde Domain, die auf 127.0.0.1 zeigt, waere
        sonst die gleiche Origin wie s3mail und duerfte das Postfach auslesen. Der
        Browser schickt in dem Fall aber ihren Namen im Host-Header mit."""
        if self.bind not in self.LOOPBACK:
            return True          # gebunden nach aussen: Auth macht der Reverse-Proxy
        host = (self.headers.get("Host") or "").strip()
        if host.startswith("["):                      # [::1]:8765
            name, _, port = host.partition("]")
            name, port = name[1:], port.lstrip(":")
        else:
            name, _, port = host.partition(":")
        return name in self.LOOPBACK and port in ("", str(self.port))

    def _origin_ok(self) -> bool:
        """Gegen CSRF: eine fremde Seite kann per fetch() einen POST hierher
        schicken (Content-Type text/plain, kein Preflight). Lesen kann sie die
        Antwort nicht, aber Loeschen und Versenden wuerden trotzdem laufen. Bei
        genau solchen Anfragen setzt der Browser die Origin."""
        origin = self.headers.get("Origin")
        if not origin or origin == "null":
            return True          # gleiche Origin oder gar kein Browser
        parts = urlparse(origin)
        return (parts.hostname in self.LOOPBACK
                and str(parts.port or "") in ("", str(self.port)))

    def _token(self) -> str:
        """Token aus Header, Query (?t=) oder Cookie."""
        given = self.headers.get("X-S3mail-Token") or ""
        if not given:
            given = parse_qs(urlparse(self.path).query).get("t", [""])[0]
        if not given:
            for part in (self.headers.get("Cookie") or "").split(";"):
                name, _, value = part.strip().partition("=")
                if name == "s3mail":
                    given = value
                    break
        return given

    def _token_ok(self) -> bool:
        """Gegen Mitleser auf demselben Rechner: 127.0.0.1 erreicht jeder lokale
        Benutzer. Das Token steht nur in der Adresse, die beim Start ausgegeben
        wird, und danach in einem SameSite-Cookie."""
        return bool(self.token) and secrets.compare_digest(self._token(), self.token)

    def _guard_request(self) -> bool:
        """True, wenn die Anfrage bedient werden darf. Sonst ist schon geantwortet."""
        if not self._host_ok():
            self._send(403, b"s3mail: unerwarteter Host-Header",
                       "text/plain; charset=utf-8")
            return False
        if not self._origin_ok():
            self._json({"error": "Anfrage von einer fremden Herkunft abgelehnt"}, 403)
            return False
        if not self._token_ok():
            if urlparse(self.path).path.startswith("/api/"):
                self._json({"error": "Token fehlt oder passt nicht"}, 403)
            else:
                self._send(403, TOKEN_PAGE.encode("utf-8"),
                           "text/html; charset=utf-8")
            return False
        return True

    def _page(self, body: str):
        """Seite ausliefern und dabei das Token als Cookie setzen, damit Anhaenge
        und Folgeaufrufe ohne ?t= in der Adresse auskommen."""
        self._send(200, body.encode("utf-8"), "text/html; charset=utf-8",
                   {"Set-Cookie": f"s3mail={self.token}; Path=/; HttpOnly; "
                                  f"SameSite=Strict"})

    # -- GET ---------------------------------------------------------------- #
    def do_GET(self):
        if not self._guard_request():
            return
        url = urlparse(self.path)
        qs = parse_qs(url.query, keep_blank_values=True)

        def run():
            if url.path in ("/", "/index.html"):
                if self.store is None:                 # noch nicht eingerichtet
                    return self._page(SETUP_PAGE)
                self._page(PAGE.replace("__CONFIG__", json.dumps(self.config)))
            elif url.path in ("/setup", "/setup/"):
                self._page(SETUP_PAGE)
            elif self.store is None:
                self._json({"error": "s3mail ist noch nicht eingerichtet"}, 503)
            elif url.path == "/api/overview":
                self._json(self._overview())
            elif url.path == "/api/messages":
                folder = qs.get("folder", ["*"])[0]
                self._json({
                    "messages": self.store.search(
                        query=qs.get("q", [""])[0],
                        folder=None if folder == "*" else valid_folder(folder),
                        tag=qs.get("tag", [""])[0] or None,
                        only_unread=qs.get("unread", ["0"])[0] == "1",
                        only_star=qs.get("star", ["0"])[0] == "1",
                    ),
                    **self._overview(),
                })
            elif url.path == "/api/message":
                self._json(self.store.message(qs["key"][0]))
            elif url.path == "/api/attachment":
                name, ctype, payload = self.store.attachment(
                    qs["key"][0], int(qs.get("index", ["0"])[0]))
                safe = re.sub(r'[^\w.\- ]', "_", name)
                self._send(200, payload, ctype or "application/octet-stream",
                           {"Content-Disposition": f'attachment; filename="{safe}"'})
            elif url.path == "/api/raw":
                payload = self.store.raw(qs["key"][0])
                fname = os.path.basename(qs["key"][0]) or "mail"
                self._send(200, payload, "message/rfc822",
                           {"Content-Disposition": f'attachment; filename="{fname}.eml"'})
            else:
                self._json({"error": "not found"}, 404)

        self._guard(run)

    # -- POST --------------------------------------------------------------- #
    def do_POST(self):
        if not self._guard_request():
            return
        url = urlparse(self.path)
        length = int(self.headers.get("Content-Length") or 0)
        raw = self.rfile.read(length) if length else b"{}"
        try:
            data = json.loads(raw.decode("utf-8") or "{}")
        except ValueError:
            return self._json({"error": "ungueltiges JSON"}, 400)
        store = self.store

        def run():
            if url.path.startswith("/api/setup/"):
                result = setup_api(url.path, data)
                if url.path == "/api/setup/save":
                    activate(result["config"], no_send=Handler.no_send)
                return self._json(result)
            if store is None:
                return self._json({"error": "s3mail ist noch nicht eingerichtet"}, 503)
            keys = data.get("keys") or ([data["key"]] if data.get("key") else [])
            mids = [store.mid(k) for k in keys]

            if url.path == "/api/refresh":
                self._json({**store.refresh(), **self._overview()})
            elif url.path == "/api/move":
                self._json({"moved": store.move(keys, data.get("folder", INBOX)),
                            **self._overview()})
            elif url.path == "/api/delete":
                self._json({"deleted": store.delete(keys), **self._overview()})
            elif url.path == "/api/empty-trash":
                self._json({"deleted": store.empty_trash(), **self._overview()})
            elif url.path == "/api/flag":
                store.state.set_flags(mids, read=data.get("read"), star=data.get("star"))
                self._json({"ok": True, **self._overview()})
            elif url.path == "/api/tag":
                store.state.set_tags(mids, add=data.get("add") or [],
                                     remove=data.get("remove") or [])
                self._json({"ok": True, **self._overview()})
            elif url.path == "/api/tags":
                action = data.get("action")
                if action == "delete":
                    store.state.delete_tag(data["name"])
                elif action in ("rename", "create"):
                    store.state.rename_tag(data.get("old") or data["name"],
                                           data["name"], data.get("color"))
                else:
                    raise ValueError("unbekannte Tag-Aktion")
                self._json({"ok": True, **self._overview()})
            elif url.path == "/api/rules":
                rules = store.state.set_rules(data.get("rules") or [])
                self._json({"rules": rules, **self._overview()})
            elif url.path == "/api/rules/apply":
                moved = store.apply_rules(force=True)
                self._json({"moved": moved, **self._overview()})
            elif url.path == "/api/send":
                if self.sender is None:
                    raise ValueError("SES-Versand ist deaktiviert (--no-send)")
                self._json({"message_id": self.sender.send(store, data)})
            else:
                self._json({"error": "not found"}, 404)

        self._guard(run)


# --------------------------------------------------------------------------- #
# Frontend
# --------------------------------------------------------------------------- #
TOKEN_PAGE = """<!doctype html>
<html lang="de"><head><meta charset="utf-8"><title>s3mail</title></head>
<body style="font:15px/1.65 -apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,sans-serif;
             max-width:34em; margin:14vh auto; padding:0 6vw; color:#16202b">
<h1 style="font-size:20px">Token fehlt</h1>
<p>s3mail ist nur über die Adresse erreichbar, die beim Start im Terminal steht –
sie enthält ein Token. Sonst käme jeder andere Benutzer dieses Rechners über
127.0.0.1 ins Postfach.</p>
<p>Falls das Postfach in einem anderen Tab offen ist: dort neu laden. Sonst die
Adresse aus dem Terminal kopieren.</p>
</body></html>"""

PAGE = r"""<!doctype html>
<html lang="de">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>s3mail</title>
<style>
  :root {
    --bg:#0f1216; --panel:#161b22; --panel2:#1c232c; --line:#2a323d;
    --text:#e6edf3; --muted:#8b98a5; --accent:#4c8dff; --danger:#f2596b;
    --radius:10px;
  }
  @media (prefers-color-scheme: light) {
    :root { --bg:#f5f7fa; --panel:#fff; --panel2:#eef2f7; --line:#dce3ec;
            --text:#16202b; --muted:#5d6b7a; }
  }
  * { box-sizing:border-box; }
  body { margin:0; height:100vh; display:flex; flex-direction:column; overflow:hidden;
         font:14px/1.5 -apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,sans-serif;
         background:var(--bg); color:var(--text); }
  header { display:flex; gap:10px; align-items:center; padding:9px 14px;
           border-bottom:1px solid var(--line); background:var(--panel); }
  header b { font-size:15px; }
  .box { font-size:12px; color:var(--muted); }
  input, textarea, button, select { font:inherit; color:var(--text); }
  #q { flex:1; padding:7px 12px; border-radius:var(--radius);
       border:1px solid var(--line); background:var(--panel2); min-width:120px; }
  #q:focus { outline:2px solid var(--accent); outline-offset:-1px; }
  button { background:var(--panel2); border:1px solid var(--line); color:var(--text);
           border-radius:var(--radius); padding:6px 11px; cursor:pointer; }
  button:hover:not(:disabled) { border-color:var(--accent); }
  button:disabled { opacity:.5; cursor:default; }
  button.primary { background:var(--accent); border-color:var(--accent); color:#fff; }
  button.danger { color:var(--danger); border-color:var(--danger); }
  button.ghost { background:none; border:none; padding:4px 6px; }
  main { flex:1; display:flex; min-height:0; }

  /* Sidebar */
  #side { width:224px; flex:none; border-right:1px solid var(--line);
          background:var(--panel); overflow-y:auto; padding:8px 0 20px; }
  .sec { font-size:11px; letter-spacing:.08em; text-transform:uppercase;
         color:var(--muted); padding:12px 14px 4px; display:flex;
         justify-content:space-between; align-items:center; }
  .nav { display:flex; align-items:center; gap:8px; padding:6px 14px; cursor:pointer;
         border-left:3px solid transparent; }
  .nav:hover { background:var(--panel2); }
  .nav.sel { background:var(--panel2); border-left-color:var(--accent); }
  .nav .name { flex:1; overflow:hidden; text-overflow:ellipsis; white-space:nowrap; }
  .nav .cnt { font-size:11.5px; color:var(--muted); }
  .nav.unread .name { font-weight:700; }
  .nav.unread .cnt { color:var(--accent); font-weight:700; }
  .dot { width:9px; height:9px; border-radius:50%; flex:none; }
  .side-in { display:flex; gap:4px; padding:4px 10px 4px 14px; }
  .side-in input { flex:1; min-width:0; padding:4px 8px; border-radius:6px;
                   border:1px solid var(--line); background:var(--panel2); }

  /* Liste */
  #listwrap { width:432px; flex:none; border-right:1px solid var(--line);
              display:flex; flex-direction:column; background:var(--panel); }
  #tools { display:flex; align-items:center; gap:5px; padding:7px 10px;
           border-bottom:1px solid var(--line); flex-wrap:wrap; min-height:44px; }
  #tools button { padding:5px 8px; }
  #tools .grow { flex:1; }
  #list { flex:1; overflow-y:auto; }
  .item { display:flex; gap:8px; padding:9px 12px; border-bottom:1px solid var(--line);
          cursor:pointer; }
  .item:hover { background:var(--panel2); }
  .item.sel { background:var(--panel2); box-shadow:inset 3px 0 0 var(--accent); }
  .item.checked { background:color-mix(in srgb, var(--accent) 12%, var(--panel)); }
  .item .col { flex:1; min-width:0; }
  .item .row { display:flex; justify-content:space-between; gap:8px; }
  .item .from { overflow:hidden; text-overflow:ellipsis; white-space:nowrap; }
  .item.unread .from, .item.unread .subj { font-weight:700; }
  .item .date { color:var(--muted); font-size:11.5px; white-space:nowrap; }
  .item .subj, .item .snip { overflow:hidden; text-overflow:ellipsis; white-space:nowrap; }
  .item .snip { color:var(--muted); font-size:12.5px; }
  .star { cursor:pointer; color:var(--muted); font-size:13px; line-height:1; }
  .star.on { color:#e0a33e; }
  .chip { font-size:10.5px; padding:0 6px; border-radius:99px; line-height:16px;
          display:inline-block; vertical-align:1px; color:#fff; }
  .tag-line { display:flex; gap:4px; flex-wrap:wrap; margin-top:3px; }
  .flag { font-size:10.5px; padding:0 6px; border-radius:99px; line-height:16px;
          border:1px solid var(--line); color:var(--muted); }
  .flag.bad { color:var(--danger); border-color:var(--danger); }

  /* Ansicht */
  #view { flex:1; overflow-y:auto; padding:18px 24px; min-width:0; }
  #view h2 { margin:0 0 6px; font-size:19px; }
  .meta { color:var(--muted); font-size:13px; }
  .bar { display:flex; gap:8px; margin:14px 0; flex-wrap:wrap; align-items:center; }
  pre.body { white-space:pre-wrap; word-wrap:break-word; font:inherit;
             background:var(--panel); border:1px solid var(--line);
             border-radius:var(--radius); padding:14px; }
  iframe.body { width:100%; height:58vh; border:1px solid var(--line);
                border-radius:var(--radius); background:#fff; }
  .atts { display:flex; flex-wrap:wrap; gap:8px; margin:12px 0; }
  .att { display:inline-flex; gap:6px; align-items:center; text-decoration:none;
         color:var(--text); background:var(--panel); border:1px solid var(--line);
         border-radius:var(--radius); padding:6px 10px; font-size:13px; }
  .att:hover { border-color:var(--accent); }
  .empty { color:var(--muted); padding:40px; text-align:center; }

  /* Menues + Dialoge */
  .menu { position:fixed; z-index:40; background:var(--panel); border:1px solid var(--line);
          border-radius:10px; box-shadow:0 10px 30px rgba(0,0,0,.35); padding:6px;
          min-width:200px; max-height:60vh; overflow:auto; }
  .menu div.opt { padding:6px 10px; border-radius:6px; cursor:pointer;
                  display:flex; gap:8px; align-items:center; }
  .menu div.opt:hover { background:var(--panel2); }
  .menu input { width:100%; padding:5px 8px; border-radius:6px;
                border:1px solid var(--line); background:var(--panel2); }
  dialog { border:1px solid var(--line); border-radius:14px; background:var(--panel);
           color:var(--text); width:min(720px,92vw); padding:0; }
  dialog::backdrop { background:rgba(0,0,0,.55); }
  .pad { padding:18px; display:flex; flex-direction:column; gap:10px; }
  .pad input, .pad textarea, .pad select { width:100%; padding:7px 10px; border-radius:8px;
      border:1px solid var(--line); background:var(--panel2); }
  .pad textarea { min-height:200px; resize:vertical; font-family:ui-monospace,monospace; }
  .pad label { font-size:12px; color:var(--muted); }
  .rule { display:grid; grid-template-columns:1fr 90px 1fr 1fr 34px; gap:6px;
          align-items:center; }
  .rule select, .rule input { padding:5px 7px; }
  .err { color:var(--danger); min-height:18px; }
  .toast { position:fixed; bottom:18px; right:18px; z-index:60; background:var(--panel);
           border:1px solid var(--line); border-left:3px solid var(--accent);
           border-radius:8px; padding:10px 14px; box-shadow:0 8px 24px rgba(0,0,0,.3); }
</style>
</head>
<body>
<header>
  <b>s3mail</b><span class="box" id="box"></span>
  <input id="q" placeholder='Suche:  rechnung · from:kunde@x.de · subject:"Angebot" · after:2026-01-01 · has:anhang · is:ungelesen · tag:wichtig'>
  <button id="rulesBtn" title="Automatische Regeln">Regeln</button>
  <button onclick="location.href='/setup'" title="Verbindung, Bucket, Papierkorb">Einstellungen</button>
  <button id="refresh">Neu laden</button>
</header>
<main>
  <div id="side"></div>
  <div id="listwrap">
    <div id="tools"></div>
    <div id="list"><div class="empty">…</div></div>
  </div>
  <div id="view"><div class="empty">Keine Mail ausgewählt</div></div>
</main>

<dialog id="dlgCompose"><form class="pad" method="dialog">
  <div><label>Von</label><input id="c_from"></div>
  <div><label>An</label><input id="c_to"></div>
  <div><label>Cc</label><input id="c_cc"></div>
  <div><label>Betreff</label><input id="c_subject"></div>
  <div><label>Text</label><textarea id="c_body"></textarea></div>
  <div id="c_err" class="err"></div>
  <div class="bar" style="justify-content:flex-end;margin:0">
    <button value="cancel">Abbrechen</button>
    <button class="primary" id="c_send" type="button">Senden</button>
  </div>
</form></dialog>

<dialog id="dlgRules"><form class="pad" method="dialog">
  <b>Automatische Regeln</b>
  <div class="box">Greifen beim Indexieren auf neue Mails: erste passende Regel gewinnt.
    Papierkorb und Spam werden nicht angefasst.</div>
  <div class="rule box"><span>enthält</span><span>im Feld</span><span>→ Ordner</span><span>→ Tags (Komma)</span><span></span></div>
  <div id="ruleRows" style="display:flex;flex-direction:column;gap:6px"></div>
  <div class="bar" style="margin:4px 0 0">
    <button type="button" id="ruleAdd">+ Regel</button>
    <button type="button" id="ruleApply">Auf alle bestehenden Mails anwenden</button>
    <span class="grow" style="flex:1"></span>
    <button value="cancel">Schließen</button>
    <button type="button" class="primary" id="ruleSave">Speichern</button>
  </div>
  <div id="r_err" class="err"></div>
</form></dialog>

<dialog id="dlgConfirm"><form class="pad" method="dialog">
  <b id="cf_title">Sicher?</b>
  <div id="cf_text" class="box"></div>
  <div class="bar" style="justify-content:flex-end;margin:0">
    <button value="cancel">Abbrechen</button>
    <button type="button" class="danger" id="cf_ok">Endgültig löschen</button>
  </div>
</form></dialog>

<script>
const CFG = __CONFIG__;
const $ = s => document.querySelector(s);
let messages = [], over = {folders:[], tags:{}, rules:[]}, current = null;
let folder = "", tagFilter = null, onlyUnread = false, onlyStar = false;
let checked = new Set(), lastIdx = null, mode = "reply", showImages = false, msg = null;

$("#box").textContent = CFG.bucket + "/" + (CFG.root || "");

const esc = s => (s ?? "").toString().replace(/[&<>"']/g, c =>
  ({"&":"&amp;","<":"&lt;",">":"&gt;",'"':"&quot;","'":"&#39;"}[c]));
const fmtDate = iso => {
  const d = new Date(iso), n = new Date();
  return d.toDateString() === n.toDateString()
    ? d.toLocaleTimeString([], {hour:"2-digit", minute:"2-digit"})
    : d.toLocaleDateString([], {day:"2-digit", month:"2-digit", year:"2-digit"});
};
const fmtSize = n => n < 1024 ? n + " B"
  : n < 1048576 ? (n/1024).toFixed(1) + " KB" : (n/1048576).toFixed(1) + " MB";
const folderLabel = f => (over.folders.find(x => x.name === f) || {}).label || f || "Posteingang";

function toast(text, bad) {
  const t = document.createElement("div");
  t.className = "toast"; t.textContent = text;
  if (bad) t.style.borderLeftColor = "var(--danger)";
  document.body.appendChild(t);
  setTimeout(() => t.remove(), 4500);
}

async function api(path, body) {
  const opts = body ? {method:"POST", headers:{"Content-Type":"application/json"},
                       body: JSON.stringify(body)} : undefined;
  const r = await fetch(path, opts);
  const d = await r.json().catch(() => ({error:"Antwort nicht lesbar"}));
  if (!r.ok || d.error) throw new Error(d.error || ("HTTP " + r.status));
  if (d.folders) { over = {folders:d.folders, tags:d.tags, rules:d.rules,
                           state_remote:d.state_remote, allow_delete:d.allow_delete}; }
  return d;
}
const guard = fn => (...a) => fn(...a).catch(e => toast(e.message, true));

/* ---------------- Sidebar ---------------- */
function renderSide() {
  const tags = Object.entries(over.tags);
  const tagCounts = {};
  messages.forEach(m => (m.tags || []).forEach(t => tagCounts[t] = (tagCounts[t]||0)+1));
  $("#side").innerHTML = `
    <div class="sec">Ordner <button class="ghost" id="addFolder" title="Neuer Ordner">+</button></div>
    <div id="folders">${over.folders.map(f => `
      <div class="nav${folder === f.name && !tagFilter ? " sel" : ""}${f.unread ? " unread" : ""}"
           data-f="${esc(f.name)}">
        <span>${f.icon}</span><span class="name">${esc(f.label)}</span>
        <span class="cnt">${f.unread ? f.unread + "/" : ""}${f.count}</span>
      </div>`).join("")}</div>
    <div class="side-in" id="newFolderBox" style="display:none">
      <input id="newFolderName" placeholder="Ordnername">
      <button id="newFolderOk">OK</button>
    </div>
    <div class="sec">Filter</div>
    <div class="nav${onlyUnread ? " sel" : ""}" id="fUnread"><span>●</span><span class="name">Ungelesen</span></div>
    <div class="nav${onlyStar ? " sel" : ""}" id="fStar"><span>★</span><span class="name">Markiert</span></div>
    <div class="sec">Tags</div>
    ${tags.length ? tags.map(([name, color]) => `
      <div class="nav${tagFilter === name ? " sel" : ""}" data-t="${esc(name)}">
        <span class="dot" style="background:${esc(color)}"></span>
        <span class="name">${esc(name)}</span>
        <span class="cnt">${tagCounts[name] || ""}</span>
        <span class="star" data-deltag="${esc(name)}" title="Tag löschen">✕</span>
      </div>`).join("") : '<div class="box" style="padding:2px 14px">noch keine</div>'}
    ${over.state_remote === false ? '<div class="sec" style="color:var(--danger)">Zustand nur lokal<br>(kein Schreibrecht im Bucket)</div>' : ""}`;

  $("#side").querySelectorAll("[data-f]").forEach(el => el.onclick = () => {
    folder = el.dataset.f; tagFilter = null; current = null; checked.clear(); load();
  });
  $("#side").querySelectorAll("[data-t]").forEach(el => el.onclick = e => {
    if (e.target.dataset.deltag) return;
    tagFilter = tagFilter === el.dataset.t ? null : el.dataset.t; load();
  });
  $("#side").querySelectorAll("[data-deltag]").forEach(el => el.onclick = guard(async e => {
    e.stopPropagation();
    await api("/api/tags", {action:"delete", name: el.dataset.deltag});
    if (tagFilter === el.dataset.deltag) tagFilter = null;
    load();
  }));
  $("#fUnread").onclick = () => { onlyUnread = !onlyUnread; load(); };
  $("#fStar").onclick = () => { onlyStar = !onlyStar; load(); };
  $("#addFolder").onclick = e => {
    e.stopPropagation();
    const box = $("#newFolderBox");
    box.style.display = box.style.display === "none" ? "flex" : "none";
    if (box.style.display === "flex") $("#newFolderName").focus();
  };
  const create = () => {
    const name = $("#newFolderName").value.trim();
    if (!name) return;
    if (!over.folders.some(f => f.name === name))
      over.folders.push({name, label:name, icon:"📁", count:0, unread:0, system:false});
    folder = name; $("#newFolderName").value = ""; load();
  };
  $("#newFolderOk").onclick = create;
  $("#newFolderName").onkeydown = e => { if (e.key === "Enter") { e.preventDefault(); create(); } };
}

/* ---------------- Werkzeugleiste ---------------- */
function renderTools() {
  const n = checked.size;
  const inTrash = folder === "trash";
  $("#tools").innerHTML = n === 0 ? `
      <label style="display:flex;gap:6px;align-items:center">
        <input type="checkbox" id="all"> <span class="box">${messages.length} Mails${tagFilter ? " · Tag: " + esc(tagFilter) : ""}</span>
      </label>
      <span class="grow"></span>
      ${inTrash && over.allow_delete ? '<button class="danger" id="emptyTrash">Papierkorb leeren</button>' : ""}
    ` : `
      <label style="display:flex;gap:6px;align-items:center">
        <input type="checkbox" id="all" checked> <span class="box"><b>${n}</b> gewählt</span>
      </label>
      <button id="mMove">Verschieben ▾</button>
      <button id="mTag">Tag ▾</button>
      <button id="mRead" title="gelesen/ungelesen">●</button>
      <button id="mStar" title="markieren">★</button>
      ${inTrash
        ? (over.allow_delete ? '<button class="danger" id="mPurge">Endgültig löschen</button>' : "")
        : '<button id="mTrash" title="in den Papierkorb">🗑</button>'}
    `;
  const all = $("#all");
  if (all) {
    all.indeterminate = n > 0 && n < messages.length;
    all.onclick = () => {
      if (checked.size) checked.clear();
      else messages.forEach(m => checked.add(m.key));
      renderList(); renderTools();
    };
  }
  const on = (id, fn) => { const el = $(id); if (el) el.onclick = fn; };
  on("#emptyTrash", () => confirmDelete(null));
  on("#mPurge", () => confirmDelete([...checked]));
  on("#mTrash", guard(async () => { await move([...checked], "trash"); }));
  on("#mRead", guard(async () => {
    const anyUnread = messages.some(m => checked.has(m.key) && !m.read);
    await api("/api/flag", {keys:[...checked], read: anyUnread});
    checked.clear(); load();
  }));
  on("#mStar", guard(async () => {
    const anyOff = messages.some(m => checked.has(m.key) && !m.star);
    await api("/api/flag", {keys:[...checked], star: anyOff});
    load();
  }));
  on("#mMove", e => folderMenu(e.target, guard(async f => { await move([...checked], f); })));
  on("#mTag", e => tagMenu(e.target, [...checked]));
}

/* ---------------- Menues ---------------- */
function closeMenus() { document.querySelectorAll(".menu").forEach(m => m.remove()); }
document.addEventListener("click", e => {
  if (!e.target.closest(".menu") && !e.target.closest("button")) closeMenus();
});

function openMenu(anchor, html) {
  closeMenus();
  const r = anchor.getBoundingClientRect();
  const m = document.createElement("div");
  m.className = "menu";
  m.innerHTML = html;
  m.style.left = Math.min(r.left, innerWidth - 240) + "px";
  m.style.top = (r.bottom + 4) + "px";
  document.body.appendChild(m);
  return m;
}

function folderMenu(anchor, pick) {
  const m = openMenu(anchor, over.folders.map(f =>
      `<div class="opt" data-f="${esc(f.name)}">${f.icon} ${esc(f.label)}</div>`).join("")
    + '<div class="opt" style="gap:4px"><input id="mkFolder" placeholder="neuer Ordner…"></div>');
  m.querySelectorAll("[data-f]").forEach(el =>
    el.onclick = () => { closeMenus(); pick(el.dataset.f); });
  m.querySelector("#mkFolder").onkeydown = e => {
    if (e.key === "Enter") { e.preventDefault(); const v = e.target.value.trim();
      if (v) { closeMenus(); pick(v); } }
  };
}

function tagMenu(anchor, keys) {
  const rows = Object.entries(over.tags).map(([name, color]) => {
    const all = messages.filter(m => keys.includes(m.key)).every(m => (m.tags||[]).includes(name));
    return `<div class="opt" data-tag="${esc(name)}" data-on="${all ? 1 : 0}">
      <span class="dot" style="background:${esc(color)}"></span>${esc(name)}
      <span style="flex:1"></span>${all ? "✓" : ""}</div>`;
  }).join("");
  const m = openMenu(anchor, rows + '<div class="opt"><input id="mkTag" placeholder="neuer Tag…"></div>');
  m.querySelectorAll("[data-tag]").forEach(el => el.onclick = guard(async () => {
    const on = el.dataset.on === "1";
    await api("/api/tag", {keys, [on ? "remove" : "add"]: [el.dataset.tag]});
    closeMenus(); load();
  }));
  m.querySelector("#mkTag").onkeydown = guard(async e => {
    if (e.key !== "Enter") return;
    e.preventDefault();
    const v = e.target.value.trim();
    if (!v) return;
    await api("/api/tag", {keys, add:[v]});
    closeMenus(); load();
  });
}

/* ---------------- Aktionen ---------------- */
async function move(keys, target) {
  const res = await api("/api/move", {keys, folder: target});
  checked.clear();
  if (keys.includes(current)) { current = null; $("#view").innerHTML = '<div class="empty">Verschoben.</div>'; }
  toast(keys.length + (keys.length === 1 ? " Mail" : " Mails") + " → " + folderLabel(target));
  await load();
}

function confirmDelete(keys) {
  const n = keys ? keys.length : (over.folders.find(f => f.name === "trash") || {}).count || 0;
  $("#cf_text").textContent = keys
    ? `${n} Mail(s) werden unwiderruflich aus S3 gelöscht.`
    : `Der Papierkorb (${n} Mails) wird unwiderruflich aus S3 gelöscht.`;
  $("#dlgConfirm").showModal();
  $("#cf_ok").onclick = guard(async () => {
    $("#dlgConfirm").close();
    const r = keys ? await api("/api/delete", {keys}) : await api("/api/empty-trash", {});
    checked.clear(); current = null;
    toast((r.deleted || 0) + " endgültig gelöscht");
    $("#view").innerHTML = '<div class="empty">Keine Mail ausgewählt</div>';
    await load();
  });
}

/* ---------------- Liste ---------------- */
function renderList() {
  const list = $("#list");
  if (!messages.length) {
    list.innerHTML = '<div class="empty">Nichts hier.<br>„Neu laden“ holt neue Mails aus S3.</div>';
    return;
  }
  list.innerHTML = messages.map((m, i) => `
    <div class="item${current === m.key ? " sel" : ""}${checked.has(m.key) ? " checked" : ""}${m.read ? "" : " unread"}" data-i="${i}">
      <input type="checkbox" ${checked.has(m.key) ? "checked" : ""} data-chk="${i}">
      <span class="star${m.star ? " on" : ""}" data-star="${i}">${m.star ? "★" : "☆"}</span>
      <div class="col">
        <div class="row"><span class="from">${esc(m.from || m.from_addr || "?")}</span>
          <span class="date">${fmtDate(m.date)}</span></div>
        <div class="subj">${esc(m.subject)}
          ${m.has_attachment ? '<span class="flag">📎</span>' : ""}
          ${m.spam ? '<span class="flag bad">Spam</span>' : ""}
          ${m.virus ? '<span class="flag bad">Virus</span>' : ""}
          ${folder === "*" || tagFilter ? `<span class="flag">${esc(folderLabel(m.folder))}</span>` : ""}</div>
        <div class="snip">${esc(m.snippet)}</div>
        ${(m.tags || []).length ? `<div class="tag-line">${m.tags.map(t =>
          `<span class="chip" style="background:${esc(over.tags[t] || "#888")}">${esc(t)}</span>`).join("")}</div>` : ""}
      </div>
    </div>`).join("");

  list.querySelectorAll(".item").forEach(el => el.onclick = e => {
    const i = +el.dataset.i;
    if (e.target.dataset.chk !== undefined) return;
    if (e.target.dataset.star !== undefined) { e.stopPropagation(); toggleStar(i); return; }
    open(messages[i].key);
  });
  list.querySelectorAll("[data-chk]").forEach(el => el.onclick = e => {
    e.stopPropagation();
    const i = +el.dataset.chk;
    if (e.shiftKey && lastIdx !== null) {
      const [a, b] = [Math.min(lastIdx, i), Math.max(lastIdx, i)];
      for (let k = a; k <= b; k++) checked.add(messages[k].key);
    } else {
      checked.has(messages[i].key) ? checked.delete(messages[i].key) : checked.add(messages[i].key);
    }
    lastIdx = i;
    renderList(); renderTools();
  });
}

const toggleStar = guard(async i => {
  const m = messages[i];
  m.star = !m.star;
  renderList();
  await api("/api/flag", {keys:[m.key], star: m.star});
  renderSide();
});

async function load() {
  const p = new URLSearchParams({folder, q: $("#q").value, unread: onlyUnread ? "1" : "0",
                                 star: onlyStar ? "1" : "0"});
  if (tagFilter) p.set("tag", tagFilter);
  if (tagFilter) p.set("folder", "*");
  const d = await api("/api/messages?" + p.toString());
  messages = d.messages;
  checked = new Set([...checked].filter(k => messages.some(m => m.key === k)));
  renderSide(); renderTools(); renderList();
}

/* ---------------- Mailansicht ---------------- */
const open = guard(async key => {
  current = key;
  $("#view").innerHTML = '<div class="empty">lädt…</div>';
  msg = await api("/api/message?key=" + encodeURIComponent(key));
  const m = msg;
  const idx = messages.findIndex(x => x.key === key);
  const wasUnread = idx >= 0 && !messages[idx].read;
  if (idx >= 0) { messages[idx].read = true; messages[idx].tags = m.tags; }
  renderList();
  if (wasUnread) api("/api/overview").then(renderSide).catch(() => {});

  const atts = m.attachments.map(a => `
    <a class="att" href="/api/attachment?key=${encodeURIComponent(key)}&index=${a.index}">
      📎 ${esc(a.filename)} <span class="date">${fmtSize(a.size)}</span></a>`).join("");

  let bodyHtml;
  if (m.html) {
    const csp = "default-src 'none'; style-src 'unsafe-inline'; font-src data:; img-src "
      + (showImages ? "* data:" : "data:");
    const doc = `<meta http-equiv="Content-Security-Policy" content="${csp}"><base target="_blank">`
      + `<style>body{font-family:sans-serif;margin:12px;color:#111}</style>` + m.html;
    bodyHtml = `<iframe class="body" sandbox="" srcdoc="${esc(doc)}"></iframe>`;
  } else {
    bodyHtml = `<pre class="body">${esc(m.text || "(kein Textinhalt)")}</pre>`;
  }

  $("#view").innerHTML = `
    <h2>${esc(m.subject)}</h2>
    <div class="meta"><b>Von:</b> ${esc(m.from)}</div>
    <div class="meta"><b>An:</b> ${esc(m.to)}${m.cc ? " · <b>Cc:</b> " + esc(m.cc) : ""}</div>
    <div class="meta">${new Date(m.date).toLocaleString()} · ${esc(folderLabel(m.folder))}
      ${m.spam ? '<span class="flag bad">Spam-Verdict FAIL</span>' : ""}
      ${m.virus ? '<span class="flag bad">Virus-Verdict FAIL</span>' : ""}</div>
    <div class="tag-line" style="margin-top:8px">${(m.tags||[]).map(t =>
      `<span class="chip" style="background:${esc(over.tags[t]||"#888")}">${esc(t)}</span>`).join("")}</div>
    <div class="bar">
      ${CFG.can_send ? '<button class="primary" id="reply">Antworten</button>' +
                       '<button id="replyall">Allen antworten</button>' +
                       '<button id="forward">Weiterleiten</button>' : ""}
      <button id="vStar">${m.star ? "★ markiert" : "☆ markieren"}</button>
      <button id="vTag">Tag ▾</button>
      <button id="vMove">Verschieben ▾</button>
      ${m.folder === "spam" ? '<button id="vHam">Kein Spam</button>'
                            : '<button id="vSpam">Spam</button>'}
      ${m.folder === "trash"
        ? (over.allow_delete ? '<button class="danger" id="vPurge">Endgültig löschen</button>' : "")
        : '<button id="vTrash">🗑 Papierkorb</button>'}
      <a class="att" href="/api/raw?key=${encodeURIComponent(key)}">⬇︎ .eml</a>
      ${m.html ? `<button id="imgs">${showImages ? "Bilder blockieren" : "Externe Bilder laden"}</button>` : ""}
    </div>
    ${atts ? `<div class="atts">${atts}</div>` : ""}
    ${bodyHtml}`;

  const on = (id, fn) => { const el = $(id); if (el) el.onclick = fn; };
  on("#imgs", () => { showImages = !showImages; open(key); });
  on("#reply", () => compose("reply", false));
  on("#replyall", () => compose("reply", true));
  on("#forward", () => compose("forward", false));
  on("#vTrash", () => move([key], "trash"));
  on("#vSpam", () => move([key], "spam"));
  on("#vHam", () => move([key], ""));
  on("#vPurge", () => confirmDelete([key]));
  on("#vMove", e => folderMenu(e.target, guard(async f => { await move([key], f); })));
  on("#vTag", e => tagMenu(e.target, [key]));
  on("#vStar", guard(async () => {
    await api("/api/flag", {keys:[key], star: !m.star});
    m.star = !m.star; open(key);
  }));
  renderSide();
});

/* ---------------- Verfassen ---------------- */
function quote(m) {
  return `\n\nAm ${new Date(m.date).toLocaleString()} schrieb ${m.from}:\n`
       + (m.text || "").split("\n").map(l => "> " + l).join("\n") + "\n";
}
function compose(kind, all) {
  const m = msg; mode = kind;
  const sub = m.subject || "";
  $("#c_from").value = CFG.default_from || "";
  $("#c_to").value = kind === "forward" ? "" : (m.reply_to || m.from);
  $("#c_cc").value = (kind !== "forward" && all) ? [m.to, m.cc].filter(Boolean).join(", ") : "";
  $("#c_subject").value = kind === "forward"
    ? (/^fwd:/i.test(sub) ? sub : "Fwd: " + sub)
    : (/^(re|aw):/i.test(sub) ? sub : "Re: " + sub);
  $("#c_body").value = kind === "forward"
    ? `\n\n--- Weitergeleitete Nachricht ---\nVon: ${m.from}\nAn: ${m.to}\nDatum: ${new Date(m.date).toLocaleString()}\nBetreff: ${sub}\n\n${m.text || ""}`
    : quote(m);
  $("#c_err").textContent = "";
  $("#dlgCompose").showModal();
  $("#c_body").setSelectionRange(0, 0);
  $("#c_body").focus();
}
$("#c_send").onclick = async () => {
  const b = $("#c_send"); b.disabled = true; b.textContent = "sendet…";
  try {
    const r = await api("/api/send", {mode, key: current, from: $("#c_from").value,
      to: $("#c_to").value, cc: $("#c_cc").value,
      subject: $("#c_subject").value, body: $("#c_body").value});
    $("#dlgCompose").close();
    toast("Gesendet · " + r.message_id);
  } catch (e) { $("#c_err").textContent = e.message; }
  finally { b.disabled = false; b.textContent = "Senden"; }
};

/* ---------------- Regeln ---------------- */
function ruleRow(r) {
  r = r || {contains:"", field:"any", folder:null, tags:[], enabled:true};
  const div = document.createElement("div");
  div.className = "rule";
  div.innerHTML = `
    <input class="r_contains" value="${esc(r.contains)}" placeholder="z.B. rechnung@">
    <select class="r_field">
      ${["any","from","to","subject"].map(f =>
        `<option value="${f}"${r.field === f ? " selected" : ""}>${
          {any:"irgendwo", from:"Von", to:"An/Cc", subject:"Betreff"}[f]}</option>`).join("")}
    </select>
    <select class="r_folder">
      <option value="-">— nicht verschieben —</option>
      ${over.folders.map(f => `<option value="${esc(f.name)}"${r.folder === f.name ? " selected" : ""}>${esc(f.label)}</option>`).join("")}
    </select>
    <input class="r_tags" value="${esc((r.tags||[]).join(", "))}" placeholder="Tags">
    <button type="button" class="ghost r_del" title="Regel entfernen">✕</button>`;
  div.querySelector(".r_del").onclick = () => div.remove();
  return div;
}
$("#rulesBtn").onclick = () => {
  const box = $("#ruleRows"); box.innerHTML = "";
  (over.rules || []).forEach(r => box.appendChild(ruleRow(r)));
  if (!over.rules.length) box.appendChild(ruleRow());
  $("#r_err").textContent = "";
  $("#dlgRules").showModal();
};
$("#ruleAdd").onclick = () => $("#ruleRows").appendChild(ruleRow());
function collectRules() {
  return [...$("#ruleRows").children].map(d => ({
    contains: d.querySelector(".r_contains").value.trim(),
    field: d.querySelector(".r_field").value,
    folder: d.querySelector(".r_folder").value,
    tags: d.querySelector(".r_tags").value.split(",").map(s => s.trim()).filter(Boolean),
    enabled: true,
  })).filter(r => r.contains);
}
$("#ruleSave").onclick = guard(async () => {
  await api("/api/rules", {rules: collectRules()});
  $("#dlgRules").close(); toast("Regeln gespeichert"); await load();
});
$("#ruleApply").onclick = guard(async () => {
  await api("/api/rules", {rules: collectRules()});
  const r = await api("/api/rules/apply", {});
  $("#dlgRules").close();
  toast("Regeln angewendet · " + (r.moved || 0) + " verschoben");
  await load();
});

/* ---------------- Start / Tastatur ---------------- */
$("#refresh").onclick = guard(async () => {
  const b = $("#refresh"); b.disabled = true; b.textContent = "lädt…";
  try {
    const s = await api("/api/refresh", {});
    if (s.error) toast(s.error, true);
    else toast((s.total ? s.total + " neu indexiert" : "Keine neuen Mails")
               + (s.moved ? " · " + s.moved + " per Regel einsortiert" : ""));
    await load();
  } finally { b.disabled = false; b.textContent = "Neu laden"; }
});

let timer;
$("#q").oninput = () => { clearTimeout(timer); timer = setTimeout(load, 200); };
document.addEventListener("keydown", e => {
  if (document.querySelector("dialog[open]")) return;
  const typing = /^(INPUT|TEXTAREA|SELECT)$/.test(document.activeElement.tagName);
  if (e.key === "/" && !typing) { e.preventDefault(); $("#q").focus(); return; }
  if (typing || !messages.length) return;
  const i = messages.findIndex(m => m.key === current);
  const go = j => { const m = messages[Math.max(0, Math.min(j, messages.length - 1))]; if (m) open(m.key); };
  if (e.key === "j" || e.key === "ArrowDown") { e.preventDefault(); go(i + 1); }
  else if (e.key === "k" || e.key === "ArrowUp") { e.preventDefault(); go(i < 0 ? 0 : i - 1); }
  else if (e.key === "x" && i >= 0) { checked.has(current) ? checked.delete(current) : checked.add(current); lastIdx = i; renderList(); renderTools(); }
  else if (e.key === "s" && i >= 0) toggleStar(i);
  else if (e.key === "e" && i >= 0) move([current], "archiv");
  else if ((e.key === "Delete" || e.key === "#") && i >= 0 && folder !== "trash") move([current], "trash");
});

load().then(() => { if (!messages.length && !over.folders.some(f => f.count)) $("#refresh").click(); })
      .catch(e => toast(e.message, true));
</script>
</body>
</html>
"""


# --------------------------------------------------------------------------- #
# CLI
# --------------------------------------------------------------------------- #
def main(argv=None):
    ap = argparse.ArgumentParser(
        description="Mail-Client fuer SES-Mails in S3. Ohne Argumente startet beim "
                    "ersten Mal der Einrichtungs-Assistent im Browser.")
    ap.add_argument("--bucket", help="S3-Bucket mit den Rohmails")
    ap.add_argument("--setup", action="store_true",
                    help="Einrichtungs-Assistent oeffnen, auch wenn schon konfiguriert")
    ap.add_argument("--prefix", help="Wurzel-Prefix, z.B. mail/ (Ordner liegen darunter)")
    ap.add_argument("--region", default=None, help="AWS-Region, z.B. eu-central-1")
    ap.add_argument("--profile", default=None, help="AWS-Profil aus ~/.aws/credentials")
    ap.add_argument("--from", dest="sender", default=None, help="Absender fuer Antworten")
    ap.add_argument("--no-send", action="store_true", help="SES-Versand deaktivieren")
    ap.add_argument("--no-delete", action="store_true",
                    help="Endgueltiges Loeschen sperren (nur Papierkorb)")
    ap.add_argument("--port", type=int, help="Standard 8765")
    ap.add_argument("--host", help="Standard 127.0.0.1")
    ap.add_argument("--no-browser", action="store_true")
    args = ap.parse_args(argv)

    try:
        import boto3  # noqa: F401
    except ImportError:
        sys.exit("boto3 fehlt:  pip install boto3")

    cfg = load_config()
    for key, val in (("bucket", args.bucket), ("prefix", args.prefix),
                     ("region", args.region), ("profile", args.profile),
                     ("from", args.sender), ("port", args.port), ("host", args.host)):
        if val is not None:
            cfg[key] = val
    if args.no_delete:
        cfg["allow_delete"] = False
    if args.prefix is not None:
        cfg["prefix"] = normalize_prefix(args.prefix)
    Handler.no_send = args.no_send

    store = None
    if cfg.get("bucket") and not args.setup:
        try:
            store = activate(cfg, no_send=args.no_send)
        except Exception as exc:
            print(f"Verbindung fehlgeschlagen ({exc}) - starte den Assistenten.")

    Handler.token = secrets.token_urlsafe(24)
    Handler.bind = cfg["host"]
    Handler.port = int(cfg["port"])
    httpd = ThreadingHTTPServer((cfg["host"], int(cfg["port"])), Handler)
    url = f"http://{cfg['host']}:{cfg['port']}/?t={Handler.token}"
    if store is None:
        print(f"s3mail ist noch nicht eingerichtet - Assistent: {url}", flush=True)
    else:
        print(f"s3mail laeuft auf {url}   (Strg+C zum Beenden)", flush=True)
        print(f"Bucket: {cfg['bucket']}/{store.root}   Cache: {store.cache_file}",
              flush=True)
    if not args.no_browser:
        threading.Timer(0.6, lambda: webbrowser.open(url)).start()
    try:
        httpd.serve_forever()
    except KeyboardInterrupt:
        print("\nTschuess.")
    finally:
        httpd.server_close()


if __name__ == "__main__":
    main()
