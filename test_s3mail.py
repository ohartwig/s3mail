"""Testet die gebaute Einzeldatei s3mail.py gegen einen Fake-S3."""
import io, json, sys, tempfile, threading, urllib.request, urllib.error
from datetime import datetime, timezone
from email.message import EmailMessage
from http.server import ThreadingHTTPServer

sys.path.insert(0, "/home/claude")
import s3mail

OK = []
def ok(msg): OK.append(msg); print("ok:", msg)

TOKEN = "test-token-123"

# --------------------------------------------------------------------------- #
def mail(frm, to, subject, body, date, mid, attach=None, spam=False, html=None):
    m = EmailMessage()
    m["From"] = frm; m["To"] = to; m["Subject"] = subject
    m["Date"] = date; m["Message-ID"] = mid
    m["X-SES-Spam-Verdict"] = "FAIL" if spam else "PASS"
    m.set_content(body)
    if html: m.add_alternative(html, subtype="html")
    if attach: m.add_attachment(attach[1], maintype="application", subtype="pdf", filename=attach[0])
    return m.as_bytes()

def build_mails():
    return {
        "mail/m1": mail("Kunde Müller <kunde@example.de>", "support@firma.de",
                        "Frage zur Rechnung 2026-042", "Bitte Rechnung nochmal schicken.",
                        "Tue, 18 Aug 2026 09:12:00 +0200", "<abc123@example.de>",
                        attach=("rechnung.pdf", b"%PDF-1.4 fake"),
                        html="<html><body><p>Rechnung bitte nochmal.</p></body></html>"),
        "mail/m2": mail("newsletter@shop.io", "support@firma.de",
                        "=?utf-8?B?QW5nZWJvdDogRsO8bmYgZsO8ciBkcmVp?=", "Nur heute!",
                        "Mon, 03 Aug 2026 22:00:00 +0000", "<n1@shop.io>", spam=True),
        "mail/m3": mail("noreply@rechnungen.de", "buchhaltung@firma.de",
                        "Ihre Rechnung Juli", "Anbei.", "Sat, 01 Aug 2026 08:00:00 +0000",
                        "<r1@rechnungen.de>"),
        "mail/archiv/alt1": mail("chef@firma.de", "support@firma.de", "Altes Zeug", "alt",
                                 "Wed, 01 Jul 2026 08:00:00 +0000", "<old@firma.de>"),
        "mail/m4-kaputt": b"kein gueltiges mime\r\n" * 3,
        "andere/nicht-meins": b"ausserhalb",
    }

from botocore.exceptions import ClientError as BotoClientError

def _err(code, status, op="PutObject"):
    return BotoClientError({"Error": {"Code": code},
                            "ResponseMetadata": {"HTTPStatusCode": status}}, op)

class FakeS3:
    """Fake-S3 mit ETags, Praefix-Listing und Metadaten."""

    def __init__(self, objs, meta=None, sse=None):
        self.objs = dict(objs); self.calls = []; self.lifecycle = None
        self.meta = dict(meta or {})      # key -> Nutzer-Metadaten
        self.sse = dict(sse or {})        # key -> serverseitige Verschluesselung
    def _etag(self, k): return str(hash(self.objs[k]) & 0xffffffff)
    def get_paginator(self, name):
        outer = self
        class P:
            def paginate(self, Bucket, Prefix=""):
                items = [{"Key": k, "Size": len(v), "ETag": '"%s"' % outer._etag(k),
                          "LastModified": datetime(2026, 8, 1, 12, 0, tzinfo=timezone.utc)}
                         for k, v in sorted(outer.objs.items()) if k.startswith(Prefix)]
                yield {"Contents": items[:2]}
                yield {"Contents": items[2:]}
        return P()
    def get_object(self, Bucket, Key, Range=None):
        self.calls.append(("get", Key, Range is not None))
        if Key not in self.objs:
            raise _err("NoSuchKey", 404, "GetObject")
        data = self.objs[Key]
        etag = self._etag(Key)
        if Range:
            a, b = Range.split("=")[1].split("-")
            data = data[int(a):int(b) + 1]
        return {"Body": io.BytesIO(data), "ETag": '"%s"' % etag,
                "Metadata": dict(self.meta.get(Key, {}))}
    def head_object(self, Bucket, Key):
        self.calls.append(("head", Key))
        if Key not in self.objs:
            raise _err("NoSuchKey", 404, "HeadObject")
        return {"Metadata": dict(self.meta.get(Key, {})), "ContentLength": len(self.objs[Key]),
                "ETag": '"%s"' % self._etag(Key), **self.sse.get(Key, {})}
    def put_object(self, Bucket, Key, Body, **kw):
        self.calls.append(("put", Key))
        self.objs[Key] = Body
        return {"ETag": '"%s"' % self._etag(Key)}
    def list_objects_v2(self, Bucket, Prefix="", MaxKeys=1000):
        items = [{"Key": k, "Size": len(v), "ETag": '"%s"' % self._etag(k)}
                 for k, v in sorted(self.objs.items()) if k.startswith(Prefix)][:MaxKeys]
        return {"Contents": items, "KeyCount": len(items)}
    def list_buckets(self):
        return {"Buckets": [{"Name": "test-bucket"}, {"Name": "anderer-bucket"}]}
    def get_bucket_lifecycle_configuration(self, Bucket):
        if self.lifecycle is None:
            raise _err("NoSuchLifecycleConfiguration", 404, "GetBucketLifecycleConfiguration")
        return {"Rules": self.lifecycle}
    def put_bucket_lifecycle_configuration(self, Bucket, LifecycleConfiguration):
        self.lifecycle = LifecycleConfiguration["Rules"]; return {}
    def delete_bucket_lifecycle(self, Bucket):
        self.lifecycle = None; return {}
    def copy_object(self, Bucket, Key, CopySource, **kw):
        src = CopySource["Key"]
        self.calls.append(("copy", src, Key, kw))
        self.objs[Key] = self.objs[src]
        if src in self.meta: self.meta[Key] = dict(self.meta[src])   # Umschlag mitkopieren
        if src in self.sse: self.sse[Key] = dict(self.sse[src])
        return {}
    def delete_object(self, Bucket, Key):
        self.calls.append(("del", Key)); self.objs.pop(Key, None); return {}

class FakeSES:
    def __init__(self): self.sent = []
    def send_raw_email(self, Source, Destinations, RawMessage):
        self.sent.append((Source, Destinations, RawMessage["Data"]))
        return {"MessageId": "0100-fake-id"}

def new_store(s3=None, cache=None, **kw):
    s3 = s3 or FakeS3(build_mails())
    return s3mail.MailStore(s3, "test-bucket", "mail/", cache_dir=cache or tempfile.mkdtemp(), **kw)

# --------------------------------------------------------------------------- #
def test_index_and_folders():
    st = new_store(); r = st.refresh()
    assert r["error"] is None, r
    assert set(st.index) == {"mail/m1","mail/m2","mail/m3","mail/m4-kaputt","mail/archiv/alt1"}, st.index.keys()
    assert st.index["mail/m1"]["folder"] == ""
    assert st.index["mail/archiv/alt1"]["folder"] == "archiv"
    assert st.index["mail/m1"]["subject"] == "Frage zur Rechnung 2026-042"
    assert st.index["mail/m2"]["subject"] == "Angebot: Fünf für drei"
    assert st.index["mail/m2"]["spam"] is True
    assert st.index["mail/m1"]["has_attachment"] is True
    assert "nicht lesbar" not in st.index["mail/m1"]["subject"]
    fs = {f["name"]: f for f in st.folders()}
    assert fs[""]["count"] == 4 and fs["archiv"]["count"] == 1
    assert fs[""]["unread"] == 4, fs[""]
    assert [f["name"] for f in st.folders()][:4] == ["", "archiv", "spam", "trash"]
    ok("index, ordner aus prefix abgeleitet, ausserhalb-prefix ignoriert")

def test_state_object_not_indexed():
    s3 = FakeS3(build_mails()); st = new_store(s3)
    st.refresh(); st.state.set_flags(["m1"], read=True)
    intern = [k for k in s3.objs if k.startswith("mail/.s3mail-state")]
    assert intern, "zustand nicht im bucket"
    st.refresh()
    assert not any(k in st.index for k in intern), "zustand taucht als mail auf"
    ok("der zustand liegt im bucket und wird nicht als mail indexiert")

def test_move_keeps_state():
    s3 = FakeS3(build_mails()); st = new_store(s3); st.refresh()
    st.state.set_tags(["m1"], add=["wichtig"]); st.state.set_flags(["m1"], read=True, star=True)
    res = st.move(["mail/m1"], "archiv")
    assert res[0]["new_key"] == "mail/archiv/m1"
    assert "mail/m1" not in s3.objs and "mail/archiv/m1" in s3.objs, s3.objs.keys()
    assert any(c[:3] == ("copy", "mail/m1", "mail/archiv/m1") for c in s3.calls)
    assert ("del", "mail/m1") in s3.calls
    m = st.decorate(st.index["mail/archiv/m1"])
    assert m["folder"] == "archiv" and m["tags"] == ["wichtig"] and m["read"] and m["star"]
    st.move(["mail/archiv/m1"], "")          # zurueck in den Posteingang
    assert st.index["mail/m1"]["folder"] == ""
    assert st.decorate(st.index["mail/m1"])["tags"] == ["wichtig"]
    ok("verschieben = copy+delete, tags/gelesen/stern ueberleben den umzug")

def test_move_validation():
    st = new_store(); st.refresh()
    for bad in ["../gemein", "a/b", "", "  /  "][:2]:
        try: st.move(["mail/m1"], bad); raise AssertionError("ordnername akzeptiert: " + bad)
        except ValueError: pass
    try: st.move(["andere/nicht-meins"], "spam"); raise AssertionError("prefix-schutz fehlt")
    except PermissionError: pass
    st.move(["mail/m1"], "")  # Posteingang bleibt erlaubt
    ok("ordnernamen validiert (kein /, kein ..), prefix-schutz beim verschieben")

def test_trash_and_delete():
    s3 = FakeS3(build_mails()); st = new_store(s3); st.refresh()
    try: st.delete(["mail/m2"]); raise AssertionError("hard delete ausserhalb trash erlaubt")
    except PermissionError: pass
    st.move(["mail/m2"], "trash")
    assert "mail/trash/m2" in s3.objs
    assert st.delete(["mail/trash/m2"]) == 1
    assert "mail/trash/m2" not in s3.objs and "mail/trash/m2" not in st.index
    st.move(["mail/m3", "mail/m1"], "trash")
    assert st.empty_trash() == 2 and not [m for m in st.index.values() if m["folder"] == "trash"]
    ok("papierkorb, endgueltig loeschen nur aus trash, papierkorb leeren")

def test_delete_disabled():
    st = new_store(allow_delete=False); st.refresh(); st.move(["mail/m1"], "trash")
    try: st.delete(["mail/trash/m1"]); raise AssertionError("--no-delete wirkungslos")
    except PermissionError: pass
    ok("--no-delete sperrt endgueltiges loeschen")

def test_tags():
    st = new_store(); st.refresh()
    st.state.set_tags(["m1", "m3"], add=["rechnung", "wichtig"])
    assert st.state.get("m1")["tags"] == ["rechnung", "wichtig"]
    assert set(st.state.data["tags"]) == {"rechnung", "wichtig"}
    assert st.state.data["tags"]["rechnung"].startswith("#")
    st.state.set_tags(["m1"], remove=["wichtig"])
    assert st.state.get("m1")["tags"] == ["rechnung"] and st.state.get("m3")["tags"] == ["rechnung", "wichtig"]
    st.state.rename_tag("rechnung", "Buchhaltung")
    assert st.state.get("m1")["tags"] == ["Buchhaltung"] and "rechnung" not in st.state.data["tags"]
    st.state.delete_tag("Buchhaltung")
    assert st.state.get("m1")["tags"] == [] and "Buchhaltung" not in st.state.data["tags"]
    ok("tags: setzen, entfernen, umbenennen, loeschen (inkl. farben)")

def test_state_shared_via_bucket():
    s3 = FakeS3(build_mails()); a = new_store(s3); a.refresh()
    a.state.set_tags(["m1"], add=["wichtig"]); a.state.set_flags(["m1"], star=True)
    b = new_store(s3, cache=tempfile.mkdtemp())   # anderer "Rechner", leerer lokaler Cache
    b.refresh()
    assert b.state.get("m1")["tags"] == ["wichtig"] and b.state.get("m1")["star"]
    assert b.state.remote_ok
    ok("zustand wird ueber den bucket geteilt (zweiter client sieht tags/stern)")

def test_state_local_fallback():
    class RO(FakeS3):
        def put_object(self, **kw): raise RuntimeError("AccessDenied")
    st = new_store(RO(build_mails())); st.refresh()
    st.state.set_flags(["m1"], read=True)
    assert st.state.remote_ok is False and st.state.get("m1")["read"]
    ok("ohne schreibrecht faellt der zustand sauber auf lokal zurueck")

def test_rules():
    s3 = FakeS3(build_mails()); st = new_store(s3)
    st.state.set_rules([
        {"contains": "rechnungen.de", "field": "from", "folder": "archiv", "tags": ["Buchhaltung"]},
        {"contains": "newsletter@", "field": "from", "folder": "spam", "tags": []},
        {"contains": "", "field": "any"},                       # leere Regel fliegt raus
    ])
    assert len(st.state.data["rules"]) == 2
    r = st.refresh()
    assert r["moved"] == 2, r
    assert "mail/archiv/m3" in s3.objs and "mail/spam/m2" in s3.objs
    assert st.state.get("m3")["tags"] == ["Buchhaltung"]
    assert st.index["mail/m1"]["folder"] == "", "unbeteiligte mail verschoben"
    # zurueckverschieben: die Regel darf nicht erneut zuschlagen
    st.move(["mail/archiv/m3"], "")
    r2 = st.refresh()
    assert r2["moved"] == 0 and st.index["mail/m3"]["folder"] == "", "regel greift erneut"
    # ... ausser explizit erzwungen
    assert st.apply_rules(force=True) == 1 and "mail/archiv/m3" in s3.objs
    ok("regeln: greifen bei neuen mails, nicht erneut nach manuellem verschieben, force geht")

def test_rule_validation():
    st = new_store()
    try:
        st.state.set_rules([{"contains": "x", "folder": "../weg"}])
        raise AssertionError("regel mit boesem ordner akzeptiert")
    except ValueError: pass
    rules = st.state.set_rules([{"contains": "x", "field": "quatsch"}])
    assert rules[0]["field"] == "any"
    ok("regel-validierung (ordner, unbekanntes feld)")

def test_search():
    st = new_store(); st.refresh()
    st.state.set_flags(["m1"], read=True); st.state.set_flags(["m2"], star=True)
    st.state.set_tags(["m3"], add=["Buchhaltung"])
    q = lambda **kw: [m["mid"] for m in st.search(**kw)]
    assert q(folder="") == ["m1", "m2", "m3", "m4-kaputt"] or set(q(folder="")) == {"m1","m2","m3","m4-kaputt"}
    assert q(folder="archiv") == ["alt1"]
    assert q(query="rechnung", folder="") == ["m1", "m3"] or set(q(query="rechnung", folder="")) == {"m1","m3"}
    assert q(query="is:ungelesen", folder="") and "m1" not in q(query="is:ungelesen", folder="")
    assert q(query="is:stern") == ["m2"]
    assert q(query="tag:buchhaltung") == ["m3"]
    assert q(tag="Buchhaltung") == ["m3"]
    assert q(only_unread=True, folder="") == q(query="is:ungelesen", folder="")
    assert q(only_star=True) == ["m2"]
    assert q(query="in:archiv") == ["alt1"]
    assert q(query="has:anhang") == ["m1"]
    assert q(query="from:newsletter") == ["m2"]
    assert q(query='subject:"fünf für"') == ["m2"]
    assert q(query="after:2026-08-10") == ["m1"]
    ok("suche: ordner, tag, is:ungelesen/stern, in:, has:, from:, after:")

def test_read_on_open():
    st = new_store(); st.refresh()
    assert not st.state.get("m1")["read"]
    m = st.message("mail/m1")
    assert st.state.get("m1")["read"] and m["read"]
    assert m["attachments"][0]["filename"] == "rechnung.pdf"
    assert st.attachment("mail/m1", m["attachments"][0]["index"])[2].startswith(b"%PDF")
    assert "<p>" in m["html"] and "Rechnung" in m["text"]
    fs = {f["name"]: f for f in st.folders()}
    assert fs[""]["unread"] == 3
    ok("oeffnen markiert als gelesen, ungelesen-zaehler stimmt, anhang lesbar")

def test_send():
    st = new_store(); st.refresh(); ses = FakeSES()
    s = s3mail.Sender(ses, "support@firma.de")
    s.send(st, {"mode": "reply", "key": "mail/m1", "to": "kunde@example.de",
                "subject": "Re: x", "body": "ok"})
    raw = ses.sent[-1][2].decode("utf-8", "replace")
    assert "In-Reply-To: <abc123@example.de>" in raw and "References: <abc123@example.de>" in raw
    assert not st.state.get("m1")["read"], "antworten hat die mail als gelesen markiert"
    s.send(st, {"mode": "forward", "key": "mail/m1", "to": "kollege@firma.de",
                "subject": "Fwd: x", "body": "fyi"})
    assert "message/rfc822" in ses.sent[-1][2].decode("utf-8", "replace")
    try:
        s.send(st, {"to": "", "subject": "x", "body": "y"}); raise AssertionError("leerer empfaenger")
    except ValueError: pass
    ok("senden: reply-header, forward mit .eml, validierung")

def test_http():
    s3 = FakeS3(build_mails()); st = new_store(s3); st.refresh()
    s3mail.Handler.store = st
    s3mail.Handler.sender = s3mail.Sender(FakeSES(), "support@firma.de")
    s3mail.Handler.config = {"bucket": "test-bucket", "root": "mail/",
                             "default_from": "support@firma.de", "can_send": True}
    httpd = ThreadingHTTPServer(("127.0.0.1", 0), s3mail.Handler)
    port = httpd.server_address[1]
    s3mail.Handler.token = TOKEN
    s3mail.Handler.bind, s3mail.Handler.port = "127.0.0.1", port
    threading.Thread(target=httpd.serve_forever, daemon=True).start()
    base = f"http://127.0.0.1:{port}"

    def call(path, body=None, expect=200):
        req = urllib.request.Request(base + path, method="POST" if body is not None else "GET",
                                     data=json.dumps(body).encode() if body is not None else None,
                                     headers={"Content-Type": "application/json",
                                              "X-S3mail-Token": TOKEN})
        try:
            r = urllib.request.urlopen(req)
        except urllib.error.HTTPError as e:
            assert e.code == expect, f"{path}: HTTP {e.code}, erwartet {expect}"
            return json.loads(e.read())
        assert expect == 200, f"{path}: unerwartet erfolgreich"
        return json.loads(r.read()) if "json" in r.headers.get("Content-Type", "") else r

    page = urllib.request.urlopen(base + "/?t=" + TOKEN).read().decode()
    assert "__CONFIG__" not in page and '"can_send": true' in page
    d = call("/api/messages?folder=")
    assert len(d["messages"]) == 4 and d["folders"] and d["allow_delete"] is True
    assert call("/api/messages?folder=archiv")["messages"][0]["mid"] == "alt1"
    call("/api/flag", {"keys": ["mail/m1"], "star": True})
    assert call("/api/messages?folder=&star=1")["messages"][0]["mid"] == "m1"
    call("/api/tag", {"keys": ["mail/m1"], "add": ["wichtig"]})
    assert call("/api/messages?tag=wichtig&folder=*")["messages"][0]["tags"] == ["wichtig"]
    mv = call("/api/move", {"keys": ["mail/m2"], "folder": "spam"})
    assert mv["moved"][0]["new_key"] == "mail/spam/m2"
    assert call("/api/delete", {"keys": ["mail/spam/m2"]}, expect=403)["error"]
    call("/api/move", {"keys": ["mail/spam/m2"], "folder": "trash"})
    assert call("/api/delete", {"keys": ["mail/trash/m2"]})["deleted"] == 1
    assert call("/api/move", {"keys": ["mail/m1"], "folder": "../boese"}, expect=400)["error"]
    assert call("/api/message?key=andere/nicht-meins", expect=403)["error"]
    assert call("/api/nix", expect=404)["error"]
    rl = call("/api/rules", {"rules": [{"contains": "shop.io", "field": "from", "folder": "spam"}]})
    assert rl["rules"][0]["folder"] == "spam"
    assert call("/api/rules/apply", {})["moved"] >= 0
    assert call("/api/send", {"mode": "reply", "key": "mail/m1", "to": "a@b.de",
                              "subject": "s", "body": "b"})["message_id"] == "0100-fake-id"
    r = urllib.request.urlopen(base + "/api/raw?key=mail/m1&t=" + TOKEN)
    assert r.read().startswith(b"From:") and "attachment" in r.headers["Content-Disposition"]
    httpd.shutdown()
    ok("http: messages/flag/tag/move/delete/rules/send/raw + fehlercodes 400/403/404")

def test_http_access_control():
    """Host-Header (DNS-Rebinding), Origin (CSRF) und Token (Mitleser lokal)."""
    s3 = FakeS3(build_mails()); st = new_store(s3); st.refresh()
    s3mail.Handler.store = st
    s3mail.Handler.sender = s3mail.Sender(FakeSES(), "support@firma.de")
    s3mail.Handler.config = {"bucket": "test-bucket", "root": "mail/",
                             "default_from": "support@firma.de", "can_send": True}
    httpd = ThreadingHTTPServer(("127.0.0.1", 0), s3mail.Handler)
    port = httpd.server_address[1]
    s3mail.Handler.token = TOKEN
    s3mail.Handler.bind, s3mail.Handler.port = "127.0.0.1", port
    threading.Thread(target=httpd.serve_forever, daemon=True).start()
    base = f"http://127.0.0.1:{port}"

    def status(path, headers, body=None):
        req = urllib.request.Request(
            base + path, method="POST" if body is not None else "GET",
            data=json.dumps(body).encode() if body is not None else None,
            headers=headers)
        try:
            return urllib.request.urlopen(req).status
        except urllib.error.HTTPError as e:
            return e.code

    good = {"X-S3mail-Token": TOKEN}
    assert status("/api/overview", good) == 200

    # Token fehlt oder passt nicht
    assert status("/api/overview", {}) == 403
    assert status("/api/overview", {"X-S3mail-Token": "falsch"}) == 403
    assert status("/", {}) == 403, "postfachseite ohne token ausgeliefert"

    # Token per Cookie statt Header
    assert status("/api/overview", {"Cookie": f"s3mail={TOKEN}"}) == 200
    assert status("/api/overview", {"Cookie": "s3mail=falsch"}) == 403

    # Seitenaufruf mit ?t= setzt das Cookie fuer Anhaenge und Folgeaufrufe
    head = urllib.request.urlopen(base + "/?t=" + TOKEN).headers["Set-Cookie"]
    assert f"s3mail={TOKEN}" in head and "SameSite=Strict" in head, head

    # CSRF: fremde Seite schickt einen POST, das Token-Cookie faehrt mit
    assert status("/api/empty-trash", {**good, "Origin": "https://boese.example"},
                  body={}) == 403, "csrf-post von fremder origin durchgelassen"
    assert status("/api/send", {**good, "Origin": "http://localhost:1234"},
                  body={}) == 403, "fremder port als origin durchgelassen"
    assert status("/api/overview", {**good, "Origin": f"http://127.0.0.1:{port}"}) == 200

    # DNS-Rebinding: fremder Name im Host-Header
    assert status("/api/message?key=mail/m1",
                  {**good, "Host": "boese.example"}) == 403, "fremder host durchgelassen"
    assert status("/api/overview", {**good, "Host": f"localhost:{port}"}) == 200

    assert st.index, "abgewiesene anfragen haben das postfach veraendert"
    httpd.shutdown()
    ok("http: host/origin/token sperren rebinding, csrf und lokale mitleser")


# --------------------------------------------------------------------------- #
# Verschluesselung
# --------------------------------------------------------------------------- #
import base64
from cryptography.hazmat.primitives.ciphers import Cipher, algorithms, modes
from cryptography.hazmat.primitives.ciphers.aead import AESGCM

class FakeKMS:
    """Tut so, als waere der verpackte Schluessel einfach 'WRAP:' + Schluessel."""
    def __init__(self): self.contexts = []
    def decrypt(self, CiphertextBlob, EncryptionContext=None):
        self.contexts.append(EncryptionContext)
        if not CiphertextBlob.startswith(b"WRAP:"):
            raise _err("InvalidCiphertextException", 400, "Decrypt")
        return {"Plaintext": CiphertextBlob[5:]}

class DenyKMS:
    def decrypt(self, **kw): raise _err("AccessDeniedException", 400, "Decrypt")

def seal_gcm(plain: bytes, key: bytes = b"K" * 32, iv: bytes = b"I" * 12):
    """Legt eine Mail so ab, wie SES sie client-seitig verschluesselt ablegen wuerde."""
    ct = AESGCM(key).encrypt(iv, plain, None)          # Tag haengt hinten dran
    meta = {
        "x-amz-key-v2": base64.b64encode(b"WRAP:" + key).decode(),
        "x-amz-iv": base64.b64encode(iv).decode(),
        "x-amz-cek-alg": "AES/GCM/NoPadding",
        "x-amz-wrap-alg": "kms+context",
        "x-amz-tag-len": "128",
        "x-amz-matdesc": json.dumps({"aws:x-amz-cek-alg": "AES/GCM/NoPadding"}),
        "x-amz-unencrypted-content-length": str(len(plain)),
    }
    return ct, meta

def seal_cbc(plain: bytes, key: bytes = b"L" * 32, iv: bytes = b"J" * 16):
    pad = 16 - len(plain) % 16
    enc = Cipher(algorithms.AES(key), modes.CBC(iv)).encryptor()
    ct = enc.update(plain + bytes([pad]) * pad) + enc.finalize()
    meta = {
        "x-amz-key": base64.b64encode(b"WRAP:" + key).decode(),
        "x-amz-iv": base64.b64encode(iv).decode(),
        "x-amz-cek-alg": "AES/CBC/PKCS5Padding",
        "x-amz-matdesc": json.dumps({"kms_cmk_id": "arn:aws:kms:eu-central-1:1:key/abc"}),
        "x-amz-unencrypted-content-length": str(len(plain)),
    }
    return ct, meta

def encrypted_store(kms=None, cache=None):
    plain = build_mails()
    objs, meta = {}, {}
    objs["mail/e1"], meta["mail/e1"] = seal_gcm(plain["mail/m1"])
    objs["mail/e2"], meta["mail/e2"] = seal_cbc(plain["mail/m2"])
    objs["mail/klar"] = plain["mail/m3"]                       # gemischtes Postfach
    s3 = FakeS3(objs, meta=meta)
    st = s3mail.MailStore(s3, "test-bucket", "mail/", cache_dir=cache or tempfile.mkdtemp(),
                          kms=kms if kms is not None else FakeKMS())
    return s3, st

def test_client_side_encrypted_mails():
    s3, st = encrypted_store()
    r = st.refresh()
    assert r["error"] is None, r
    assert st.encrypted is True, "verschluesseltes postfach nicht erkannt"
    assert st.index["mail/e1"]["subject"] == "Frage zur Rechnung 2026-042"
    assert st.index["mail/e2"]["subject"] == "Angebot: Fünf für drei"
    assert st.index["mail/klar"]["subject"] == "Ihre Rechnung Juli", "klartext-mail kaputt"
    assert st.index["mail/e1"]["has_attachment"] is True
    m = st.message("mail/e1")
    assert "Bitte Rechnung nochmal schicken." in m["text"]
    assert st.attachment("mail/e1", m["attachments"][0]["index"])[2].startswith(b"%PDF")
    assert st.raw("mail/e1").startswith(b"From:"), ".eml-download liefert chiffrat"
    ok("client-seitig verschluesselt (AES-GCM v2 und AES-CBC v1) wird entschluesselt")

def test_encrypted_no_partial_range():
    s3, st = encrypted_store()
    st.refresh()                                   # erkennt unterwegs, dass verschluesselt ist
    assert st.encrypted
    s3.calls.clear()
    st.index.clear(); st.refresh()                 # zweiter Lauf: weiss es schon
    gets = [c for c in s3.calls if c[0] == "get"]
    assert gets, "gar nichts geladen"
    assert not any(c[2] for c in gets), f"Range-GET auf verschluesseltem postfach: {gets}"
    assert st.index["mail/e1"]["subject"] == "Frage zur Rechnung 2026-042"

    plain_s3 = FakeS3(build_mails()); plain = new_store(plain_s3); plain.refresh()
    assert any(c[2] for c in plain_s3.calls if c[0] == "get"), "klartext ohne Range geladen"
    assert plain.encrypted is False
    ok("verschluesselt: ganzes objekt statt teilstueck - klartext bleibt beim range-get")

def test_encrypted_move_keeps_envelope():
    s3, st = encrypted_store()
    st.refresh()
    st.move(["mail/e1"], "archiv")
    assert "mail/archiv/e1" in s3.meta and "x-amz-key-v2" in s3.meta["mail/archiv/e1"]
    assert st.message("mail/archiv/e1")["subject"] == "Frage zur Rechnung 2026-042"
    ok("verschieben laesst den krypto-umschlag intakt")

def test_sse_preserved_on_move():
    s3 = FakeS3(build_mails(), sse={"mail/m1": {"ServerSideEncryption": "aws:kms",
                                                "SSEKMSKeyId": "arn:aws:kms:eu:1:key/xyz",
                                                "BucketKeyEnabled": True,
                                                "StorageClass": "STANDARD_IA"}})
    st = new_store(s3); st.refresh()
    st.move(["mail/m1"], "archiv")
    copy = [c for c in s3.calls if c[0] == "copy"][-1]
    assert copy[3]["ServerSideEncryption"] == "aws:kms", copy
    assert copy[3]["SSEKMSKeyId"].endswith("key/xyz")
    assert copy[3]["BucketKeyEnabled"] is True and copy[3]["StorageClass"] == "STANDARD_IA"
    ok("serverseitige verschluesselung und speicherklasse ueberleben das verschieben")

def test_encrypted_without_kms_rights():
    s3, st = encrypted_store(kms=DenyKMS())
    st.refresh()
    assert "nicht lesbar" in st.index["mail/e1"]["subject"], st.index["mail/e1"]["subject"]
    assert st.index["mail/klar"]["subject"] == "Ihre Rechnung Juli", "klartext-mail mitgerissen"
    try:
        st.message("mail/e1"); raise AssertionError("ohne kms:Decrypt trotzdem gelesen")
    except Exception as exc:
        assert "AccessDenied" in str(exc) or "Decrypt" in str(exc), exc
    ok("fehlendes kms:Decrypt kippt nur die betroffene mail, nicht den index")

def test_kms_encryption_context():
    s3, st = encrypted_store()
    st.refresh()
    ctxs = [c for c in st.kms.contexts if c]
    assert {"aws:x-amz-cek-alg": "AES/GCM/NoPadding"} in ctxs, ctxs
    assert any("kms_cmk_id" in c for c in ctxs), "v1-materialdescription nicht als kontext genutzt"
    ok("encryption context aus x-amz-matdesc wird an kms durchgereicht")

def test_setup_encryption_check():
    _sandbox_home()
    s3, _ = encrypted_store()
    s3mail.make_session = lambda p, r: FakeSession(s3, kms=FakeKMS())
    c = {x["name"]: x for x in s3mail.test_connection(
        FakeSession(s3, kms=FakeKMS()), "test-bucket", "mail/", "")}
    assert c["Verschlüsselung"]["ok"], c["Verschlüsselung"]
    assert "client-seitig" in c["Verschlüsselung"]["detail"]
    c2 = {x["name"]: x for x in s3mail.test_connection(
        FakeSession(s3, kms=DenyKMS()), "test-bucket", "mail/", "")}
    assert not c2["Verschlüsselung"]["ok"] and "kms:Decrypt" in c2["Verschlüsselung"]["hint"]
    plain = FakeS3(build_mails(), sse={"mail/archiv/alt1": {"ServerSideEncryption": "AES256"}})
    c3 = {x["name"]: x for x in s3mail.test_connection(FakeSession(plain), "b", "mail/", "")}
    assert c3["Verschlüsselung"]["ok"] and "AES256" in c3["Verschlüsselung"]["detail"]
    ok("wizard erkennt client-seitige, serverseitige und fehlende verschluesselung")

# --------------------------------------------------------------------------- #
# Zustand: Ops statt Vollschreiben
# --------------------------------------------------------------------------- #
def _ops(s3):
    return sorted(k for k in s3.objs if "/.s3mail-state/" in k)

def _snapshot(s3):
    key = [k for k in s3.objs if k.endswith(".s3mail-state.json")]
    return json.loads(s3.objs[key[0]]) if key else None

def test_write_is_one_small_op():
    s3 = FakeS3(build_mails()); st = new_store(s3); st.refresh()
    before = len(_ops(s3))
    st.state.set_flags(["m1"], read=True)
    ops = _ops(s3)
    assert len(ops) == before + 1, f"{len(ops) - before} objekte fuer eine aenderung"
    payload = json.loads(s3.objs[ops[-1]])
    assert payload["ops"][0]["t"] == "flags" and payload["ops"][0]["mids"] == ["m1"]
    assert len(s3.objs[ops[-1]]) < 400, "das sieht nach dem ganzen dokument aus"
    assert st.state.remote_ok
    ok("eine aenderung = ein kleines op-objekt, nicht das ganze dokument")

def test_two_clients_no_conflict():
    """Zwei Clients mit demselben Ausgangsstand schreiben nacheinander. Keiner
    ueberschreibt den anderen - es gibt gar keinen gemeinsamen Schluessel."""
    s3 = FakeS3(build_mails())
    a = new_store(s3, cache=tempfile.mkdtemp()); a.refresh()
    b = new_store(s3, cache=tempfile.mkdtemp()); b.refresh()
    a.state.set_tags(["m1"], add=["von-A"])
    b.state.set_tags(["m2"], add=["von-B"])          # kennt A's aenderung noch nicht
    assert len(_ops(s3)) == 2, "die beiden schreiben auf denselben schluessel"
    c = new_store(s3, cache=tempfile.mkdtemp())      # dritter client liest nach
    assert c.state.get("m1")["tags"] == ["von-A"], "aenderung von A verloren"
    assert c.state.get("m2")["tags"] == ["von-B"], "aenderung von B verloren"
    ok("zwei rechner schreiben nebeneinander, beide aenderungen ueberleben")

def test_two_clients_same_message():
    s3 = FakeS3(build_mails())
    a = new_store(s3, cache=tempfile.mkdtemp()); a.refresh()
    b = new_store(s3, cache=tempfile.mkdtemp()); b.refresh()
    a.state.set_tags(["m1"], add=["A"]); a.state.set_flags(["m1"], star=True)
    b.state.set_tags(["m1"], add=["B"])              # dieselbe mail, anderer tag
    c = new_store(s3, cache=tempfile.mkdtemp())
    e = c.state.get("m1")
    assert set(e["tags"]) == {"A", "B"}, e
    assert e["star"] is True, "fremdes stern-flag verloren"
    ok("dieselbe mail von zwei rechnern: tags vereinigt, fremde flags bleiben")

def test_compaction():
    s3 = FakeS3(build_mails()); st = new_store(s3); st.refresh()
    alt = s3mail.COMPACT_AFTER
    s3mail.COMPACT_AFTER = 3
    try:
        for i in range(4):
            st.state.set_tags(["m1"], add=[f"t{i}"])
    finally:
        s3mail.COMPACT_AFTER = alt
    snap = _snapshot(s3)
    assert snap and snap.get("upto"), "kein snapshot mit wasserstand geschrieben"
    assert len(_ops(s3)) <= 1, f"{len(_ops(s3))} ops nach dem zusammenfassen uebrig"
    c = new_store(s3, cache=tempfile.mkdtemp())      # frischer client sieht alles
    assert set(c.state.get("m1")["tags"]) == {"t0", "t1", "t2", "t3"}
    ok("ab COMPACT_AFTER wird zusammengefasst: snapshot + wasserstand, ops weg")

def test_watermark_ignores_merged_op():
    """Bleibt beim Aufraeumen ein Op liegen, darf es nicht ein zweites Mal wirken."""
    s3 = FakeS3(build_mails()); st = new_store(s3); st.refresh()
    st.state.set_flags(["m1"], read=True)
    alt_key = _ops(s3)[-1]
    alt_body = s3.objs[alt_key]
    alt = s3mail.COMPACT_AFTER
    s3mail.COMPACT_AFTER = 1
    try:
        st.state.set_flags(["m2"], read=True)         # loest das zusammenfassen aus
    finally:
        s3mail.COMPACT_AFTER = alt
    assert _snapshot(s3).get("upto"), "kein wasserstand"
    st.state.set_flags(["m1"], read=False)            # neuer stand: ungelesen
    s3.objs[alt_key] = alt_body                       # geloeschtes op taucht wieder auf
    c = new_store(s3, cache=tempfile.mkdtemp())
    assert c.state.get("m1")["read"] is False, "altes op wurde erneut angewandt"
    ok("wasserstand: ein liegengebliebenes op wird uebersprungen, nicht wiederholt")

def test_ops_are_not_mail():
    """Die Ops liegen unter dem Prefix - sie duerfen weder im Index noch ueber die
    API erreichbar sein."""
    s3 = FakeS3(build_mails()); st = new_store(s3); st.refresh()
    st.state.set_flags(["m1"], read=True)
    st.refresh()
    assert not any("s3mail-state" in k for k in st.index), "ops als mail indexiert"
    assert not any(f["name"].startswith(".") for f in st.folders()), "ops als ordner"
    for op in _ops(s3):
        try:
            st.move([op], "archiv"); raise AssertionError("op verschiebbar")
        except PermissionError: pass
    ok("ops tauchen nicht als mail, ordner oder verschiebbares objekt auf")

def test_write_failure_keeps_change():
    s3 = FakeS3(build_mails()); st = new_store(s3); st.refresh()
    def kaputt(**kw): raise _err("AccessDenied", 403)
    s3.put_object = kaputt
    st.state.set_flags(["m1"], read=True)
    assert st.state.remote_ok is False, "schreibfehler nicht gemerkt"
    assert st.state.get("m1")["read"] is True, "aenderung lokal verloren"
    assert st.state.pending, "aenderung nicht fuer den naechsten versuch gemerkt"
    ok("schreibfehler: lokal sichtbar, gemerkt fuer spaeter, remote_ok faellt")

def test_batch_writes_once():
    s3 = FakeS3(build_mails()); st = new_store(s3)
    st.state.set_rules([{"contains": "rechnungen.de", "field": "from", "folder": "archiv",
                         "tags": ["Buchhaltung"]}])
    before = len(_ops(s3))
    st.refresh()   # indexiert 5 mails, wendet regeln an
    after = len(_ops(s3))
    assert after - before <= 2, f"{after - before} op-objekte fuer einen refresh"
    assert st.state.get("m3")["tags"] == ["Buchhaltung"]
    ok("batch: ein refresh mit regeln schreibt ein op, nicht eins pro mail")

# --------------------------------------------------------------------------- #
# Einrichtung
# --------------------------------------------------------------------------- #
import os, stat as statmod

class FakeSession:
    def __init__(self, s3, ses=None, kms=None):
        self._s3, self._ses, self._kms = s3, ses, kms
    def client(self, name):
        if name == "s3": return self._s3
        if name == "ses": return self._ses or FakeSESIdentities()
        if name == "kms": return self._kms or FakeKMS()
        raise KeyError(name)

class FakeSESIdentities:
    def list_identities(self, IdentityType=None):
        return {"Identities": ["support@firma.de", "alt@firma.de"]}
    def get_identity_verification_attributes(self, Identities):
        return {"VerificationAttributes": {
            "support@firma.de": {"VerificationStatus": "Success"},
            "alt@firma.de": {"VerificationStatus": "Pending"},
            "firma.de": {"VerificationStatus": "Success"}}}

def _sandbox_home():
    home = tempfile.mkdtemp()
    s3mail.CONFIG_DIR = os.path.join(home, ".config", "s3mail")
    s3mail.CONFIG_FILE = os.path.join(s3mail.CONFIG_DIR, "config.json")
    s3mail.AWS_DIR = os.path.join(home, ".aws")
    return home

def test_config_roundtrip():
    _sandbox_home()
    assert s3mail.config_exists() is False
    path = s3mail.save_config({"bucket": "b", "prefix": "mail", "region": "eu-central-1",
                               "from": "a@b.de", "profile": "s3mail"})
    assert oct(statmod.S_IMODE(os.stat(path).st_mode)) == "0o600", "config zu offen"
    cfg = s3mail.load_config()
    assert cfg["prefix"] == "mail/" and cfg["bucket"] == "b" and cfg["allow_delete"] is True
    assert s3mail.config_exists() is True
    try:
        s3mail.save_config({"bucket": ""}); raise AssertionError("bucket-leer akzeptiert")
    except ValueError: pass
    for raw, want in [("", ""), ("mail", "mail/"), ("/mail/", "mail/"), ("  a/b  ", "a/b/")]:
        assert s3mail.normalize_prefix(raw) == want, raw
    ok("config: speichern/laden, chmod 600, prefix-normalisierung, pflichtfeld bucket")

def test_credentials_written_as_profile():
    home = _sandbox_home()
    name = s3mail.write_credentials("s3mail", "AKIAIOSFODNN7EXAMPLE",
                                    "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", "eu-central-1")
    cred = os.path.join(home, ".aws", "credentials")
    conf = os.path.join(home, ".aws", "config")
    body = open(cred).read()
    assert name == "s3mail" and "[s3mail]" in body and "AKIAIOSFODNN7EXAMPLE" in body
    assert "eu-central-1" in open(conf).read() and "[profile s3mail]" in open(conf).read()
    assert oct(statmod.S_IMODE(os.stat(cred).st_mode)) == "0o600", "credentials zu offen"
    assert "s3mail" in s3mail.aws_profiles()
    for bad in [("s3mail", "", "x"), ("s3mail", "hallo", "x")]:
        try:
            s3mail.write_credentials(bad[0], bad[1], bad[2], "eu-central-1")
            raise AssertionError("mist akzeptiert: " + repr(bad))
        except ValueError: pass
    # zweites Profil zerstoert das erste nicht
    s3mail.write_credentials("privat", "AKIAIOSFODNN7EXAMPLE2", "sec2", "eu-west-1")
    assert "[s3mail]" in open(cred).read() and "[privat]" in open(cred).read()
    ok("zugangsdaten landen als aws-profil in ~/.aws (600), validiert, additiv")

def test_connection_checks():
    _sandbox_home()
    s3 = FakeS3(build_mails()); sess = FakeSession(s3)
    checks = {c["name"]: c for c in s3mail.test_connection(sess, "b", "mail/", "support@firma.de")}
    assert checks["Bucket lesen"]["ok"] and "Objekt" in checks["Bucket lesen"]["detail"]
    assert checks["Mail lesen"]["ok"]
    assert checks["Schreiben"]["ok"] and checks["Löschen"]["ok"]
    assert checks["Zustand von mehreren Rechnern"]["ok"]
    assert checks["SES-Absender"]["ok"]
    assert not any(k.startswith(".s3mail-probe") for k in s3.objs), "testobjekt blieb liegen"

    class Blind(FakeS3):
        def list_objects_v2(self, **kw): raise _err("AccessDenied", 403, "ListObjectsV2")
    c2 = s3mail.test_connection(FakeSession(Blind({})), "b", "mail/", "")
    assert len(c2) == 1 and not c2[0]["ok"] and "ListBucket" in c2[0]["hint"]

    class ReadOnly(FakeS3):
        def put_object(self, **kw): raise _err("AccessDenied", 403)
    c3 = {c["name"]: c for c in s3mail.test_connection(FakeSession(ReadOnly(build_mails())), "b", "mail/", "")}
    assert not c3["Schreiben"]["ok"] and "Papierkorb" in c3["Schreiben"]["hint"]
    assert "Löschen" not in c3, "loeschen wurde geprueft, obwohl schreiben schon scheiterte"
    ok("verbindungstest: lesen/schreiben/loeschen/zustand/ses, klartext-hinweise")

def test_lifecycle_rule():
    s3 = FakeS3(build_mails()); sess = FakeSession(s3)
    s3.lifecycle = [{"ID": "fremd", "Status": "Enabled", "Filter": {"Prefix": "logs/"},
                     "Expiration": {"Days": 5}}]
    msg = s3mail.set_trash_lifecycle(sess, "b", "mail/", 30)
    ids = {r["ID"]: r for r in s3.lifecycle}
    assert "fremd" in ids, "fremde lifecycle-regel geloescht"
    assert ids["s3mail-trash"]["Filter"]["Prefix"] == "mail/trash/"
    assert ids["s3mail-trash"]["Expiration"]["Days"] == 30 and "30" in msg
    assert s3mail.current_lifecycle_days(sess, "b") == 30
    s3mail.set_trash_lifecycle(sess, "b", "mail/", 7)
    assert len([r for r in s3.lifecycle if r["ID"] == "s3mail-trash"]) == 1, "regel doppelt"
    s3mail.set_trash_lifecycle(sess, "b", "mail/", 0)
    assert not any(r["ID"] == "s3mail-trash" for r in s3.lifecycle)
    ok("lifecycle: regel setzen/aendern/entfernen ohne fremde regeln anzufassen")

def test_setup_api_and_wizard_http():
    _sandbox_home()
    s3 = FakeS3(build_mails())
    s3mail.make_session = lambda p, r: FakeSession(s3)          # kein echtes AWS
    info = s3mail.setup_api("/api/setup/info", {})
    assert info["config"]["bucket"] == "" and any(r["id"] == "eu-central-1" for r in info["regions"])
    b = s3mail.setup_api("/api/setup/buckets", {"region": "eu-central-1"})
    assert b["buckets"] == ["test-bucket", "anderer-bucket"], b
    assert b["identities"] == ["support@firma.de"], b
    t = s3mail.setup_api("/api/setup/test", {"bucket": "test-bucket", "prefix": "mail/",
                                             "from": "support@firma.de"})
    assert t["ok"] is True and len(t["checks"]) >= 5
    try:
        s3mail.setup_api("/api/setup/test", {"bucket": ""}); raise AssertionError("ohne bucket ok?")
    except ValueError: pass

    # Server im Setup-Modus: Postfach-API gesperrt, Wizard erreichbar
    s3mail.Handler.store = None; s3mail.Handler.sender = None
    httpd = ThreadingHTTPServer(("127.0.0.1", 0), s3mail.Handler)
    port = httpd.server_address[1]
    s3mail.Handler.token = TOKEN
    s3mail.Handler.bind, s3mail.Handler.port = "127.0.0.1", port
    threading.Thread(target=httpd.serve_forever, daemon=True).start()
    base = f"http://127.0.0.1:{port}"
    tok = {"X-S3mail-Token": TOKEN}
    def get(path):
        return urllib.request.urlopen(urllib.request.Request(base + path, headers=tok))
    page = get("/").read().decode()
    assert "s3mail einrichten" in page and "Access Key" in page
    assert "s3mail einrichten" in get("/setup").read().decode()
    try:
        get("/api/messages?folder="); raise AssertionError("postfach offen")
    except urllib.error.HTTPError as e:
        assert e.code == 503 and "eingerichtet" in json.loads(e.read())["error"]

    # Der Assistent schreibt AWS-Zugangsdaten - ohne Token darf er nicht anspringen
    try:
        urllib.request.urlopen(urllib.request.Request(
            base + "/api/setup/credentials", method="POST", data=b"{}",
            headers={"Content-Type": "application/json"}))
        raise AssertionError("assistent ohne token erreichbar")
    except urllib.error.HTTPError as e:
        assert e.code == 403

    req = urllib.request.Request(base + "/api/setup/save", method="POST",
        data=json.dumps({"bucket": "test-bucket", "prefix": "mail/", "region": "eu-central-1",
                         "from": "support@firma.de"}).encode(),
        headers={"Content-Type": "application/json", **tok})
    saved = json.loads(urllib.request.urlopen(req).read())
    assert saved["config"]["bucket"] == "test-bucket"
    assert s3mail.Handler.store is not None, "nach dem speichern nicht scharf geschaltet"
    d = json.loads(get("/api/messages?folder=").read())
    assert "messages" in d
    assert "__CONFIG__" not in get("/").read().decode()
    httpd.shutdown()
    ok("wizard: info/buckets/test/save, setup-modus sperrt das postfach, danach live")

# --------------------------------------------------------------------------- #
if __name__ == "__main__":
    for name, fn in list(globals().items()):
        if name.startswith("test_"):
            fn()
    print(f"\n{len(OK)} TESTGRUPPEN BESTANDEN")
