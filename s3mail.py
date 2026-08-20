#!/usr/bin/env python3
"""
s3mail - Mail-Client fuer E-Mails, die Amazon SES in einen S3-Bucket legt.

Lokaler Webserver (nur 127.0.0.1), Postfach im Browser: Ordner als echte
S3-Prefixe, Papierkorb, Spam, Tags, gelesen/ungelesen, Stern, Massenaktionen,
Auto-Regeln, Antworten/Weiterleiten ueber SES.

    pip install boto3
    python3 s3mail.py --bucket mein-mail-bucket --prefix mail/ \
        --region eu-central-1 --from support@example.com

Benoetigte IAM-Rechte:
    s3:ListBucket    s3:GetObject    s3:PutObject    s3:DeleteObject
    ses:SendRawEmail (nur fuer Antworten/Weiterleiten)

Erzeugt aus s3mail_core.py + s3mail_web.py - nicht direkt editieren.
"""

from __future__ import annotations

import configparser
import json
import os
import stat
import argparse
import json
import os
import re
import sys
import threading
import traceback
import webbrowser
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, urlparse
import base64
import email
import email.policy
import html
import json
import os
import re
import threading
import traceback
from concurrent.futures import ThreadPoolExecutor
from datetime import datetime, timezone
from email.header import decode_header, make_header
from email.message import EmailMessage
from email.utils import formatdate, getaddresses, make_msgid, parsedate_to_datetime

try:
    from botocore.exceptions import ClientError
except ImportError:  # pragma: no cover
    class ClientError(Exception):  # minimaler Ersatz, damit Tests ohne boto3 laufen
        response: dict = {}

try:
    from cryptography.hazmat.primitives.ciphers import Cipher, algorithms, modes
    from cryptography.hazmat.primitives.ciphers.aead import AESGCM
    HAVE_CRYPTO = True
except ImportError:  # pragma: no cover
    HAVE_CRYPTO = False

HEADER_CHUNK = 65536      # Bytes, die fuer den Index per Range-GET geholt werden
POLICY = email.policy.default
STATE_OBJECT = ".s3mail-state.json"

INBOX = ""                # Posteingang = Objekte direkt unter dem Root-Prefix
TRASH = "trash"
SPAM = "spam"
ARCHIVE = "archiv"
SYSTEM_FOLDERS = [
    (INBOX, "Posteingang", "\U0001F4E5"),
    (ARCHIVE, "Archiv", "\U0001F4E6"),
    (SPAM, "Spam", "⚠️"),
    (TRASH, "Papierkorb", "\U0001F5D1️"),
]
FOLDER_RE = re.compile(r"^[^\W\d_][\w .-]{0,39}$", re.UNICODE)
TAG_COLORS = ["#4c8dff", "#38b48b", "#e0a33e", "#d9534f", "#a06ee1", "#3ea8c4", "#e07a5f", "#7f9c3a"]


# --------------------------------------------------------------------------- #
# Hilfsfunktionen
# --------------------------------------------------------------------------- #
def dec(value: str | None) -> str:
    """MIME-kodierte Header (=?UTF-8?B?...?=) in lesbaren Text wandeln."""
    if not value:
        return ""
    try:
        return str(make_header(decode_header(value))).strip()
    except Exception:
        return str(value).strip()


def addr_list(value: str | None) -> list[dict]:
    out = []
    for name, addr in getaddresses([value or ""]):
        if not addr and not name:
            continue
        out.append({"name": dec(name), "addr": addr})
    return out


def addr_str(value: str | None) -> str:
    parts = []
    for a in addr_list(value):
        parts.append(f'{a["name"]} <{a["addr"]}>' if a["name"] else a["addr"])
    return ", ".join(parts)


def parse_date(msg, fallback: datetime) -> datetime:
    raw = msg.get("Date")
    if raw:
        try:
            d = parsedate_to_datetime(raw)
            if d is not None:
                return d if d.tzinfo else d.replace(tzinfo=timezone.utc)
        except Exception:
            pass
    return fallback


def part_text(part) -> str:
    """Payload eines Parts als str dekodieren, ohne bei kaputtem Charset zu sterben."""
    try:
        payload = part.get_payload(decode=True)
    except Exception:
        return ""
    if payload is None:
        return ""
    charset = part.get_content_charset() or "utf-8"
    try:
        return payload.decode(charset, errors="replace")
    except (LookupError, TypeError, ValueError):
        return payload.decode("utf-8", errors="replace")


def strip_html(raw: str) -> str:
    raw = re.sub(r"(?is)<(script|style|head).*?</\1>", " ", raw)
    raw = re.sub(r"(?i)<br\s*/?>|</p>|</div>|</tr>", "\n", raw)
    raw = re.sub(r"(?s)<[^>]+>", " ", raw)
    raw = html.unescape(raw)
    return re.sub(r"[ \t\r\f\v]+", " ", raw).strip()


def is_attachment(part) -> bool:
    disp = (part.get_content_disposition() or "").lower()
    if disp == "attachment":
        return True
    if part.get_filename():
        return True
    return disp == "inline" and not part.get_content_type().startswith("text/")


def walk_parts(msg):
    """(index, part) fuer alle Blatt-Parts."""
    idx = 0
    for part in msg.walk():
        if part.get_content_maintype() == "multipart":
            continue
        yield idx, part
        idx += 1


# --------------------------------------------------------------------------- #
# Verschluesselung
# --------------------------------------------------------------------------- #
# SES kann eingehende Mails client-seitig mit KMS verschluesseln, bevor sie in S3
# landen. Dann liegt im Bucket kein MIME, sondern ein Umschlag: der Datenschluessel
# steckt (von KMS verpackt) in den Objekt-Metadaten, der Inhalt ist AES-verschluesselt.
# Serverseitige Verschluesselung (SSE-S3, SSE-KMS) braucht dagegen nichts davon -
# die macht S3 beim GET selbst rueckgaengig.
CSE_KEY_V2 = "x-amz-key-v2"
CSE_KEY_V1 = "x-amz-key"


def lower_meta(meta: dict | None) -> dict:
    return {str(k).lower(): v for k, v in (meta or {}).items()}


def is_encrypted_envelope(meta: dict | None) -> bool:
    m = lower_meta(meta)
    return CSE_KEY_V2 in m or CSE_KEY_V1 in m


def _unpad(data: bytes) -> bytes:
    if not data:
        return data
    n = data[-1]
    return data[:-n] if 0 < n <= 16 and data[-n:] == bytes([n]) * n else data


def decrypt_envelope(body: bytes, meta: dict, kms) -> bytes:
    """Client-seitig verschluesseltes Objekt aufmachen (KMS + AES-GCM bzw. AES-CBC)."""
    m = lower_meta(meta)
    wrapped_b64 = m.get(CSE_KEY_V2) or m.get(CSE_KEY_V1)
    if not wrapped_b64:
        return body
    if kms is None:
        raise PermissionError("Diese Mail ist mit KMS verschluesselt, aber es ist kein "
                              "KMS-Zugriff eingerichtet.")
    if not HAVE_CRYPTO:
        raise RuntimeError("Zum Entschluesseln fehlt das Paket 'cryptography' "
                           "(pip install cryptography).")
    try:
        context = json.loads(m.get("x-amz-matdesc") or "{}")
    except ValueError:
        context = {}
    resp = kms.decrypt(CiphertextBlob=base64.b64decode(wrapped_b64),
                       EncryptionContext=context)
    key = resp["Plaintext"]
    iv = base64.b64decode(m.get("x-amz-iv") or "")
    alg = (m.get("x-amz-cek-alg") or "AES/CBC/PKCS5Padding").upper()

    if "GCM" in alg:                       # Tag haengt hinten am Chiffrat
        plain = AESGCM(key).decrypt(iv, body, None)
    else:
        dec = Cipher(algorithms.AES(key), modes.CBC(iv)).decryptor()
        plain = _unpad(dec.update(body) + dec.finalize())

    want = m.get("x-amz-unencrypted-content-length")
    if want and str(len(plain)) != str(want):
        raise ValueError(f"Entschluesselte Laenge passt nicht ({len(plain)} != {want})")
    return plain


def valid_folder(name: str) -> str:
    """Ordnernamen pruefen. Leerstring = Posteingang."""
    name = (name or "").strip().strip("/")
    if name == "":
        return INBOX
    if "/" in name or name in (".", "..") or not FOLDER_RE.match(name):
        raise ValueError(f"Ungueltiger Ordnername: {name!r}")
    return name


# --------------------------------------------------------------------------- #
# Zustand: Tags, gelesen, Stern, Regeln
# --------------------------------------------------------------------------- #
def _default_state() -> dict:
    return {"messages": {}, "tags": {}, "rules": []}


def _blank_entry() -> dict:
    return {"read": False, "star": False, "tags": [], "ruled": False}


def apply_op(data: dict, op: dict) -> None:
    """Eine Zustandsaenderung auf ein Zustands-Dict anwenden (rein, ohne I/O).

    Dadurch laesst sich eine Aenderung nach einem Schreibkonflikt einfach auf dem
    frisch geladenen Fremdstand wiederholen, statt ihn zu ueberschreiben.
    """
    msgs, tags = data.setdefault("messages", {}), data.setdefault("tags", {})
    entry = lambda mid: msgs.setdefault(mid, _blank_entry())
    t = op.get("t")

    if t == "flags":
        for mid in op["mids"]:
            e = entry(mid)
            if op.get("read") is not None:
                e["read"] = bool(op["read"])
            if op.get("star") is not None:
                e["star"] = bool(op["star"])
    elif t == "tags":
        for tag in op.get("add", []):
            tags.setdefault(tag, TAG_COLORS[len(tags) % len(TAG_COLORS)])
        for mid in op["mids"]:
            e = entry(mid)
            cur = [x for x in e.get("tags", []) if x not in op.get("remove", [])]
            for tag in op.get("add", []):
                if tag not in cur:
                    cur.append(tag)
            e["tags"] = cur
    elif t == "ruled":
        for mid in op["mids"]:
            entry(mid)["ruled"] = True
    elif t == "drop":
        for mid in op["mids"]:
            msgs.pop(mid, None)
    elif t == "rekey":
        if op["old"] in msgs:
            msgs[op["new"]] = dict(msgs[op["old"]])
    elif t == "tagdel":
        tags.pop(op["name"], None)
        for e in msgs.values():
            if op["name"] in e.get("tags", []):
                e["tags"] = [x for x in e["tags"] if x != op["name"]]
    elif t == "tagren":
        old, new, color = op["old"], op["new"], op.get("color")
        if old != new and old in tags:
            tags[new] = color or tags.pop(old)
        elif color or new not in tags:
            tags[new] = color or tags.get(new) or TAG_COLORS[len(tags) % len(TAG_COLORS)]
        if old != new:
            for e in msgs.values():
                e["tags"] = [new if x == old else x for x in e.get("tags", [])]
    elif t == "rules":
        data["rules"] = op["rules"]


def _merge_missing(base: dict, other: dict) -> dict:
    """Eintraege aus `other` uebernehmen, die `base` gar nicht kennt."""
    if not isinstance(other, dict):
        return base
    for mid, e in (other.get("messages") or {}).items():
        base.setdefault("messages", {}).setdefault(mid, e)
    for tag, color in (other.get("tags") or {}).items():
        base.setdefault("tags", {}).setdefault(tag, color)
    if not base.get("rules") and other.get("rules"):
        base["rules"] = other["rules"]
    return base


class Batch:
    """Sammelt mehrere Aenderungen und schreibt erst am Ende einmal nach S3."""

    def __init__(self, state): self.state = state

    def __enter__(self):
        with self.state.lock:
            self.state._defer += 1
        return self.state

    def __exit__(self, *exc):
        with self.state.lock:
            self.state._defer -= 1
            flush = self.state._defer == 0 and self.state._dirty
        if flush:
            self.state.save()
        return False


class State:
    """
    Tags, gelesen/ungelesen, Stern und Regeln. Liegt als JSON im Bucket
    (<root>.s3mail-state.json), damit mehrere Rechner denselben Stand sehen.
    Schluessel ist der Basename des S3-Objekts, nicht der volle Key - so
    ueberlebt der Zustand das Verschieben zwischen Ordnern.

    Geschrieben wird mit Conditional Write (If-Match auf das ETag, If-None-Match: *
    beim ersten Anlegen). Hat in der Zwischenzeit jemand anderes geschrieben,
    antwortet S3 mit 412; dann wird der fremde Stand geladen und die eigenen,
    noch nicht bestaetigten Aenderungen werden darauf wiederholt. Nichts wird
    blind ueberschrieben.
    """

    MAX_ATTEMPTS = 5

    def __init__(self, s3, bucket: str, root: str, local_file: str):
        self.s3 = s3
        self.bucket = bucket
        self.key = root + STATE_OBJECT
        self.local_file = local_file
        self.lock = threading.RLock()
        self.data = _default_state()
        self.etag: str | None = None
        self.pending: list[dict] = []      # lokal angewandt, noch nicht in S3 bestaetigt
        self.conditional = True            # faellt auf False, wenn S3 das nicht kann
        self.remote_ok = True
        self.conflicts = 0
        self._defer = 0
        self._dirty = False
        self.load()

    # -- Laden -------------------------------------------------------------- #
    def _fetch(self) -> tuple[dict | None, str | None]:
        try:
            r = self.s3.get_object(Bucket=self.bucket, Key=self.key)
            data = json.loads(r["Body"].read().decode("utf-8"))
            etag = (r.get("ETag") or "").strip('"') or None
            return (data if isinstance(data, dict) else None), etag
        except Exception:
            return None, None

    def _read_local(self) -> dict | None:
        try:
            with open(self.local_file, "r", encoding="utf-8") as fh:
                data = json.load(fh)
            return data if isinstance(data, dict) else None
        except (OSError, ValueError):
            return None

    def load(self):
        with self.lock:
            local = self._read_local()
            remote, etag = self._fetch()
            if remote is not None:
                base = remote
                self.etag = etag
                if local:
                    _merge_missing(base, local)   # offline Gemachtes nicht verlieren
            else:
                base = local or _default_state()
                self.etag = None
            base.setdefault("messages", {})
            base.setdefault("tags", {})
            base.setdefault("rules", [])
            self.data = base
            for op in self.pending:               # eigene offene Aenderungen erneut drauf
                apply_op(self.data, op)

    # -- Schreiben ---------------------------------------------------------- #
    def batch(self) -> Batch:
        return Batch(self)

    def _write_local(self, payload: bytes):
        try:
            tmp = self.local_file + ".tmp"
            with open(tmp, "wb") as fh:
                fh.write(payload)
            os.replace(tmp, self.local_file)
        except OSError:
            pass

    def _put(self, payload: bytes):
        kwargs = {}
        if self.conditional:
            if self.etag:
                kwargs["IfMatch"] = self.etag
            else:
                kwargs["IfNoneMatch"] = "*"
        return self.s3.put_object(Bucket=self.bucket, Key=self.key, Body=payload,
                                  ContentType="application/json", **kwargs)

    def save(self) -> bool:
        with self.lock:
            if self._defer:
                self._dirty = True
                return True
            for _ in range(self.MAX_ATTEMPTS):
                payload = json.dumps(self.data, ensure_ascii=False).encode("utf-8")
                self._write_local(payload)
                try:
                    resp = self._put(payload) or {}
                    self.etag = (resp.get("ETag") or "").strip('"') or None
                    if self.etag is None:         # Antwort ohne ETag -> nachschlagen
                        _, self.etag = self._fetch()
                    self.pending.clear()
                    self.remote_ok = True
                    self._dirty = False
                    return True
                except ClientError as exc:
                    err = getattr(exc, "response", {}).get("Error", {}) or {}
                    code = str(err.get("Code", ""))
                    status = str(getattr(exc, "response", {})
                                 .get("ResponseMetadata", {}).get("HTTPStatusCode", ""))
                    if code in ("PreconditionFailed", "ConditionalRequestConflict",
                                "OperationAborted") or status in ("412", "409"):
                        self.conflicts += 1
                        remote, etag = self._fetch()   # fremden Stand holen ...
                        self.data = remote or _default_state()
                        self.data.setdefault("messages", {})
                        self.data.setdefault("tags", {})
                        self.data.setdefault("rules", [])
                        self.etag = etag
                        for op in self.pending:        # ... und eigene Aenderungen wiederholen
                            apply_op(self.data, op)
                        continue
                    if code in ("NotImplemented", "InvalidRequest", "InvalidArgument",
                                "MethodNotAllowed") or status == "501":
                        self.conditional = False       # S3-Klon ohne Conditional Writes
                        continue
                    self.remote_ok = False
                    return False
                except Exception:
                    self.remote_ok = False
                    return False
            self.remote_ok = False
            return False

    def _mutate(self, op: dict) -> bool:
        with self.lock:
            apply_op(self.data, op)
            self.pending.append(op)
            self._dirty = True
        return self.save()

    # -- Zugriff ------------------------------------------------------------ #
    def get(self, mid: str) -> dict:
        return self.data["messages"].get(mid, _blank_entry())

    def entry(self, mid: str) -> dict:
        with self.lock:
            return self.data["messages"].setdefault(mid, _blank_entry())

    def set_flags(self, mids, read=None, star=None):
        if mids:
            self._mutate({"t": "flags", "mids": list(mids), "read": read, "star": star})

    def set_tags(self, mids, add=(), remove=()):
        add = [t for t in (add or []) if t]
        remove = [t for t in (remove or []) if t]
        if mids and (add or remove):
            self._mutate({"t": "tags", "mids": list(mids), "add": add, "remove": remove})

    def mark_ruled(self, mids):
        if mids:
            self._mutate({"t": "ruled", "mids": list(mids)})

    def drop(self, mids):
        if mids:
            self._mutate({"t": "drop", "mids": list(mids)})

    def rekey(self, old: str, new: str):
        self._mutate({"t": "rekey", "old": old, "new": new})

    def delete_tag(self, tag: str):
        self._mutate({"t": "tagdel", "name": tag})

    def rename_tag(self, old: str, new: str, color: str | None = None):
        self._mutate({"t": "tagren", "old": old, "new": new, "color": color})

    def set_rules(self, rules: list[dict]):
        clean = []
        for r in rules or []:
            field = (r.get("field") or "any").lower()
            if field not in ("from", "to", "subject", "any"):
                field = "any"
            contains = (r.get("contains") or "").strip()
            if not contains:
                continue
            folder = r.get("folder")
            clean.append({
                "name": (r.get("name") or contains)[:60],
                "field": field,
                "contains": contains,
                "folder": valid_folder(folder) if folder not in (None, "-") else None,
                "tags": [t for t in (r.get("tags") or []) if t][:5],
                "star": bool(r.get("star")),
                "read": bool(r.get("read")),
                "enabled": r.get("enabled", True),
            })
        self._mutate({"t": "rules", "rules": clean})
        return clean


# --------------------------------------------------------------------------- #
# Index / Store
# --------------------------------------------------------------------------- #
class MailStore:
    def __init__(self, s3, bucket: str, root: str, cache_dir: str | None = None,
                 allow_delete: bool = True, kms=None):
        self.s3 = s3
        self.kms = kms
        self.encrypted = False        # merkt sich, ob das Postfach CSE-verschluesselt ist
        self.bucket = bucket
        self.root = (root or "").lstrip("/")
        if self.root and not self.root.endswith("/"):
            self.root += "/"
        self.allow_delete = allow_delete
        self.lock = threading.Lock()
        self.index: dict[str, dict] = {}
        self.status = {"state": "idle", "done": 0, "total": 0, "error": None, "moved": 0}

        base = cache_dir or os.path.join(
            os.environ.get("XDG_CACHE_HOME", os.path.expanduser("~/.cache")), "s3mail"
        )
        os.makedirs(base, exist_ok=True)
        slug = re.sub(r"[^A-Za-z0-9_.-]", "_", f"{bucket}__{self.root}") or "default"
        self.cache_file = os.path.join(base, slug + ".json")
        self.state = State(s3, bucket, self.root, os.path.join(base, slug + ".state.json"))
        self._load_cache()

    # -- Keys / Ordner ------------------------------------------------------ #
    def mid(self, key: str) -> str:
        return key.rsplit("/", 1)[-1]

    def folder_of(self, key: str) -> str:
        rest = key[len(self.root):]
        return rest.rsplit("/", 1)[0] if "/" in rest else INBOX

    def key_for(self, mid: str, folder: str) -> str:
        folder = valid_folder(folder)
        return f"{self.root}{folder + '/' if folder else ''}{mid}"

    def _own(self, key: str):
        if not key.startswith(self.root):
            raise PermissionError("Key liegt ausserhalb des Prefix")
        if self.mid(key).startswith("."):
            raise PermissionError("Interne Datei")

    def folders(self) -> list[dict]:
        counts: dict[str, dict] = {}
        for m in self.index.values():
            f = m.get("folder", INBOX)
            c = counts.setdefault(f, {"count": 0, "unread": 0})
            c["count"] += 1
            if not self.state.get(m["mid"]).get("read"):
                c["unread"] += 1
        known = {f for f, _, _ in SYSTEM_FOLDERS} | set(counts)
        out = []
        for name, label, icon in SYSTEM_FOLDERS:
            c = counts.get(name, {"count": 0, "unread": 0})
            out.append({"name": name, "label": label, "icon": icon, "system": True, **c})
        for name in sorted(known - {f for f, _, _ in SYSTEM_FOLDERS}):
            c = counts.get(name, {"count": 0, "unread": 0})
            out.append({"name": name, "label": name, "icon": "\U0001F4C1", "system": False, **c})
        return out

    # -- Cache -------------------------------------------------------------- #
    def _load_cache(self):
        try:
            with open(self.cache_file, "r", encoding="utf-8") as fh:
                data = json.load(fh)
            if isinstance(data, dict):
                self.index = data.get("messages", {})
        except (OSError, ValueError):
            self.index = {}

    def _save_cache(self):
        tmp = self.cache_file + ".tmp"
        try:
            with open(tmp, "w", encoding="utf-8") as fh:
                json.dump({"version": 2, "messages": self.index}, fh)
            os.replace(tmp, self.cache_file)
        except OSError:
            pass

    # -- Lesen (entschluesselt, falls noetig) -------------------------------- #
    def fetch(self, key: str, head_bytes: int | None = None) -> bytes:
        """Objekt holen. Bei client-seitig verschluesselten Mails wird immer das
        ganze Objekt geladen und entschluesselt - ein Teilstueck laesst sich nicht
        aufmachen."""
        kwargs = {"Bucket": self.bucket, "Key": key}
        partial = head_bytes and not self.encrypted
        if partial:
            kwargs["Range"] = f"bytes=0-{head_bytes - 1}"
        try:
            resp = self.s3.get_object(**kwargs)
        except ClientError as exc:
            code = getattr(exc, "response", {}).get("Error", {}).get("Code", "")
            if partial and code in ("InvalidRange", "InvalidArgument"):
                resp = self.s3.get_object(Bucket=self.bucket, Key=key)
                partial = False
            else:
                raise
        body = resp["Body"].read()
        meta = resp.get("Metadata") or {}
        if not is_encrypted_envelope(meta):
            return body
        self.encrypted = True
        if partial:                    # Teilstueck taugt nicht - nochmal ganz holen
            resp = self.s3.get_object(Bucket=self.bucket, Key=key)
            body = resp["Body"].read()
            meta = resp.get("Metadata") or meta
        return decrypt_envelope(body, meta, self.kms)

    def _copy_args(self, key: str) -> dict:
        """Verschluesselung und Speicherklasse beim Verschieben mitnehmen."""
        try:
            head = self.s3.head_object(Bucket=self.bucket, Key=key)
        except Exception:
            return {}
        args = {}
        if head.get("ServerSideEncryption"):
            args["ServerSideEncryption"] = head["ServerSideEncryption"]
        if head.get("SSEKMSKeyId"):
            args["SSEKMSKeyId"] = head["SSEKMSKeyId"]
        if head.get("BucketKeyEnabled"):
            args["BucketKeyEnabled"] = True
        if head.get("StorageClass") and head["StorageClass"] != "STANDARD":
            args["StorageClass"] = head["StorageClass"]
        return args

    # -- Indexierung -------------------------------------------------------- #
    def _summarize(self, key: str, etag: str, size: int, last_modified: datetime) -> dict:
        """Nur die ersten Bytes holen und daraus Header + Vorschau bauen."""
        chunk = self.fetch(key, head_bytes=HEADER_CHUNK)
        msg = email.message_from_bytes(chunk, policy=POLICY)
        date = parse_date(msg, last_modified)

        snippet, has_att = "", False
        try:
            for _, part in walk_parts(msg):
                if is_attachment(part):
                    has_att = True
                    continue
                if not snippet and part.get_content_type() == "text/plain":
                    snippet = part_text(part)
                elif not snippet and part.get_content_type() == "text/html":
                    snippet = strip_html(part_text(part))
        except Exception:
            pass
        snippet = re.sub(r"\s+", " ", snippet).strip()[:240]

        return {
            "key": key,
            "mid": self.mid(key),
            "folder": self.folder_of(key),
            "etag": etag,
            "size": size,
            "date": date.astimezone(timezone.utc).isoformat(),
            "from": addr_str(msg.get("From")),
            "from_addr": (addr_list(msg.get("From")) or [{"addr": ""}])[0]["addr"],
            "to": addr_str(msg.get("To")),
            "cc": addr_str(msg.get("Cc")),
            "subject": dec(msg.get("Subject")) or "(kein Betreff)",
            "message_id": (msg.get("Message-ID") or "").strip(),
            "snippet": snippet,
            "has_attachment": has_att,
            "spam": (msg.get("X-SES-Spam-Verdict") or "").upper() == "FAIL",
            "virus": (msg.get("X-SES-Virus-Verdict") or "").upper() == "FAIL",
        }

    def _placeholder(self, key, etag, size, lm, exc) -> dict:
        return {
            "key": key, "mid": self.mid(key), "folder": self.folder_of(key),
            "etag": etag, "size": size,
            "date": lm.astimezone(timezone.utc).isoformat(),
            "from": "", "from_addr": "", "to": "", "cc": "",
            "subject": f"(nicht lesbar: {exc})", "message_id": "",
            "snippet": "", "has_attachment": False, "spam": False, "virus": False,
        }

    def refresh(self, workers: int = 8, apply_rules: bool = True) -> dict:
        with self.lock:
            if self.status["state"] == "running":
                return dict(self.status)
            self.status = {"state": "running", "done": 0, "total": 0, "error": None, "moved": 0}

        moved = 0
        try:
            self.state.load()
            listed: dict[str, tuple[str, int, datetime]] = {}
            paginator = self.s3.get_paginator("list_objects_v2")
            for page in paginator.paginate(Bucket=self.bucket, Prefix=self.root):
                for obj in page.get("Contents", []):
                    key = obj["Key"]
                    if key.endswith("/") or obj["Size"] == 0:
                        continue
                    if self.mid(key).startswith("."):      # state.json & Co.
                        continue
                    lm = obj["LastModified"]
                    if lm.tzinfo is None:
                        lm = lm.replace(tzinfo=timezone.utc)
                    listed[key] = (obj["ETag"].strip('"'), obj["Size"], lm)

            for gone in set(self.index) - set(listed):
                self.index.pop(gone, None)

            todo = [(k, v) for k, v in listed.items()
                    if k not in self.index or self.index[k].get("etag") != v[0]]
            with self.lock:
                self.status["total"] = len(todo)

            def work(item):
                key, (etag, size, lm) = item
                try:
                    return key, self._summarize(key, etag, size, lm)
                except Exception as exc:   # eine kaputte Mail kippt nicht den Lauf
                    return key, self._placeholder(key, etag, size, lm, exc)

            fresh = []
            if todo:
                with ThreadPoolExecutor(max_workers=workers) as pool:
                    for key, summary in pool.map(work, todo):
                        self.index[key] = summary
                        fresh.append(summary)
                        with self.lock:
                            self.status["done"] += 1

            if apply_rules and fresh:
                moved = self.apply_rules(fresh)

            self._save_cache()
            with self.lock:
                self.status = {"state": "idle", "done": len(todo), "total": len(todo),
                               "error": None, "moved": moved}
        except Exception as exc:
            traceback.print_exc()
            with self.lock:
                self.status = {"state": "idle", "done": 0, "total": 0, "moved": 0,
                               "error": f"{type(exc).__name__}: {exc}"}
        return dict(self.status)

    # -- Regeln ------------------------------------------------------------- #
    def _rule_hits(self, rule: dict, m: dict) -> bool:
        needle = rule["contains"].lower()
        field = rule["field"]
        if field == "from":
            hay = m.get("from", "")
        elif field == "to":
            hay = m.get("to", "") + " " + m.get("cc", "")
        elif field == "subject":
            hay = m.get("subject", "")
        else:
            hay = " ".join(str(m.get(f, "")) for f in ("from", "to", "cc", "subject", "snippet"))
        return needle in hay.lower()

    def apply_rules(self, messages: list[dict] | None = None, force: bool = False) -> int:
        rules = [r for r in self.state.data.get("rules", []) if r.get("enabled", True)]
        if not rules:
            return 0
        pool = messages if messages is not None else list(self.index.values())
        moved = 0
        with self.state.batch():                       # ein Schreibvorgang fuer den ganzen Lauf
            for m in list(pool):
                if self.state.get(m["mid"]).get("ruled") and not force:
                    continue
                if m.get("folder") in (TRASH, SPAM) and not force:
                    self.state.mark_ruled([m["mid"]])
                    continue
                hit = next((r for r in rules if self._rule_hits(r, m)), None)
                self.state.mark_ruled([m["mid"]])
                if not hit:
                    continue
                if hit["tags"]:
                    self.state.set_tags([m["mid"]], add=hit["tags"])
                if hit["star"] or hit["read"]:
                    self.state.set_flags([m["mid"]],
                                         read=True if hit["read"] else None,
                                         star=True if hit["star"] else None)
                if hit["folder"] is not None and hit["folder"] != m.get("folder"):
                    try:
                        self.move([m["key"]], hit["folder"])
                        moved += 1
                    except Exception:
                        traceback.print_exc()
        return moved

    # -- Verschieben / Loeschen --------------------------------------------- #
    def move(self, keys: list[str], folder: str) -> list[dict]:
        folder = valid_folder(folder)
        result = []
        with self.state.batch():
          for key in keys:
            self._own(key)
            if key not in self.index:
                continue
            if self.folder_of(key) == folder:
                result.append({"key": key, "new_key": key, "skipped": True})
                continue
            mid = self.mid(key)
            new_key = self.key_for(mid, folder)
            n = 1
            while new_key in self.index:                # Namenskollision entschaerfen
                stem, dot, ext = mid.partition(".")
                new_key = self.key_for(f"{stem}-{n}{dot}{ext}", folder)
                n += 1
            self.s3.copy_object(
                Bucket=self.bucket, Key=new_key,
                CopySource={"Bucket": self.bucket, "Key": key},
                **self._copy_args(key),      # SSE-Einstellung + Speicherklasse mitnehmen
            )
            self.s3.delete_object(Bucket=self.bucket, Key=key)
            entry = self.index.pop(key)
            entry = dict(entry, key=new_key, mid=self.mid(new_key), folder=folder)
            self.index[new_key] = entry
            if self.mid(new_key) != mid:                # Zustand mitziehen
                self.state.rekey(mid, self.mid(new_key))
            result.append({"key": key, "new_key": new_key, "folder": folder})
        self._save_cache()
        return result

    def delete(self, keys: list[str], force: bool = False) -> int:
        if not self.allow_delete:
            raise PermissionError("Endgueltiges Loeschen ist deaktiviert (--no-delete)")
        gone = 0
        with self.state.batch():
            for key in keys:
                self._own(key)
                if not force and self.folder_of(key) != TRASH:
                    raise PermissionError("Endgueltig loeschen geht nur aus dem Papierkorb")
                self.s3.delete_object(Bucket=self.bucket, Key=key)
                self.index.pop(key, None)
                self.state.drop([self.mid(key)])
                gone += 1
        self._save_cache()
        return gone

    def empty_trash(self) -> int:
        keys = [k for k, m in self.index.items() if m.get("folder") == TRASH]
        return self.delete(keys)

    # -- Abfragen ----------------------------------------------------------- #
    def decorate(self, m: dict) -> dict:
        st = self.state.get(m["mid"])
        return dict(m, read=st.get("read", False), star=st.get("star", False),
                    tags=st.get("tags", []))

    def search(self, query: str = "", folder: str | None = None, tag: str | None = None,
               only_unread: bool = False, only_star: bool = False) -> list[dict]:
        terms, filters = [], []
        for token in re.findall(r'\w+:"[^"]*"|\w+:\S+|"[^"]*"|\S+', query or ""):
            m = re.match(r'^(from|to|subject|betreff|after|before|has|tag|is|in)\s*:\s*"?([^"]*)"?$',
                         token.strip(), re.I)
            if m:
                filters.append((m.group(1).lower(), m.group(2).lower()))
            else:
                terms.append(token.strip('"').lower())

        def matches(m: dict) -> bool:
            hay = " ".join(str(m.get(f, "")) for f in
                           ("from", "to", "cc", "subject", "snippet", "key")).lower()
            hay += " " + " ".join(m.get("tags", [])).lower()
            if any(t not in hay for t in terms):
                return False
            for field, val in filters:
                if field == "from" and val not in m.get("from", "").lower():
                    return False
                if field == "to" and val not in (m.get("to", "") + m.get("cc", "")).lower():
                    return False
                if field in ("subject", "betreff") and val not in m.get("subject", "").lower():
                    return False
                if field == "after" and m.get("date", "")[:10] < val:
                    return False
                if field == "before" and m.get("date", "")[:10] > val:
                    return False
                if field == "tag" and val not in [t.lower() for t in m.get("tags", [])]:
                    return False
                if field == "in" and val != (m.get("folder") or "posteingang").lower():
                    return False
                if field == "has":
                    if val.startswith(("att", "anh")) and not m.get("has_attachment"):
                        return False
                    if val == "spam" and not m.get("spam"):
                        return False
                if field == "is":
                    if val in ("ungelesen", "unread") and m.get("read"):
                        return False
                    if val in ("gelesen", "read") and not m.get("read"):
                        return False
                    if val in ("stern", "star", "starred") and not m.get("star"):
                        return False
            return True

        hits = []
        for raw in self.index.values():
            if folder is not None and raw.get("folder", INBOX) != folder:
                continue
            m = self.decorate(raw)
            if only_unread and m["read"]:
                continue
            if only_star and not m["star"]:
                continue
            if tag and tag not in m["tags"]:
                continue
            if matches(m):
                hits.append(m)
        hits.sort(key=lambda m: m.get("date", ""), reverse=True)
        return hits

    # -- Inhalte ------------------------------------------------------------ #
    def raw(self, key: str) -> bytes:
        self._own(key)
        return self.fetch(key)

    def message(self, key: str, mark_read: bool = True) -> dict:
        msg = email.message_from_bytes(self.raw(key), policy=POLICY)
        text_parts, html_parts, attachments = [], [], []
        for idx, part in walk_parts(msg):
            if is_attachment(part):
                try:
                    payload = part.get_payload(decode=True) or b""
                except Exception:
                    payload = b""
                attachments.append({
                    "index": idx,
                    "filename": dec(part.get_filename()) or f"anhang-{idx}",
                    "content_type": part.get_content_type(),
                    "size": len(payload),
                })
                continue
            ctype = part.get_content_type()
            if ctype == "text/plain":
                text_parts.append(part_text(part))
            elif ctype == "text/html":
                html_parts.append(part_text(part))

        body_html = "\n<hr>\n".join(html_parts)
        body_text = "\n\n".join(text_parts)
        if not body_text and body_html:
            body_text = strip_html(body_html)

        mid = self.mid(key)
        if mark_read:
            self.state.set_flags([mid], read=True)
        st = self.state.get(mid)
        summary = self.index.get(key, {})
        fallback = summary.get("date", "1970-01-01T00:00:00+00:00")

        return {
            "key": key,
            "mid": mid,
            "folder": self.folder_of(key),
            "subject": dec(msg.get("Subject")) or "(kein Betreff)",
            "from": addr_str(msg.get("From")),
            "reply_to": addr_str(msg.get("Reply-To")),
            "to": addr_str(msg.get("To")),
            "cc": addr_str(msg.get("Cc")),
            "date": parse_date(msg, datetime.fromisoformat(fallback))
                        .astimezone(timezone.utc).isoformat(),
            "message_id": (msg.get("Message-ID") or "").strip(),
            "references": (msg.get("References") or "").strip(),
            "spam": (msg.get("X-SES-Spam-Verdict") or "").upper() == "FAIL",
            "virus": (msg.get("X-SES-Virus-Verdict") or "").upper() == "FAIL",
            "read": st.get("read", False),
            "star": st.get("star", False),
            "tags": st.get("tags", []),
            "text": body_text,
            "html": body_html,
            "attachments": attachments,
        }

    def attachment(self, key: str, index: int) -> tuple[str, str, bytes]:
        msg = email.message_from_bytes(self.raw(key), policy=POLICY)
        for idx, part in walk_parts(msg):
            if idx != index:
                continue
            payload = part.get_payload(decode=True) or b""
            name = dec(part.get_filename()) or f"anhang-{idx}"
            name = os.path.basename(name.replace("\\", "/")) or f"anhang-{idx}"
            return name, part.get_content_type(), payload
        raise KeyError("Anhang nicht gefunden")


# --------------------------------------------------------------------------- #
# Senden ueber SES
# --------------------------------------------------------------------------- #
class Sender:
    def __init__(self, ses, default_from: str | None):
        self.ses = ses
        self.default_from = default_from

    def send(self, store: MailStore, data: dict) -> str:
        mode = data.get("mode", "reply")
        key = data.get("key")
        original = store.message(key, mark_read=False) if key else {}

        sender = (data.get("from") or self.default_from or "").strip()
        if not sender:
            raise ValueError("Kein Absender gesetzt (--from oder Feld 'Von')")
        to = [a["addr"] for a in addr_list(data.get("to"))]
        cc = [a["addr"] for a in addr_list(data.get("cc"))]
        if not to and not cc:
            raise ValueError("Kein Empfaenger angegeben")

        msg = EmailMessage()
        msg["From"] = sender
        msg["To"] = ", ".join(to)
        if cc:
            msg["Cc"] = ", ".join(cc)
        msg["Subject"] = data.get("subject") or ""
        msg["Date"] = formatdate(localtime=True)
        msg["Message-ID"] = make_msgid(domain=sender.split("@")[-1] if "@" in sender else None)

        original_mid = original.get("message_id")
        if mode == "reply" and original_mid:
            msg["In-Reply-To"] = original_mid
            msg["References"] = " ".join(
                x for x in [original.get("references", ""), original_mid] if x).strip()

        msg.set_content(data.get("body") or "")

        if mode == "forward" and key:
            try:
                msg.add_attachment(
                    store.raw(key), maintype="message", subtype="rfc822",
                    filename=(original.get("subject", "mail")[:60] or "mail") + ".eml",
                )
            except Exception:
                pass

        resp = self.ses.send_raw_email(
            Source=sender, Destinations=to + cc, RawMessage={"Data": msg.as_bytes()})
        return resp.get("MessageId", "")


CONFIG_DIR = os.path.join(
    os.environ.get("XDG_CONFIG_HOME", os.path.expanduser("~/.config")), "s3mail")
CONFIG_FILE = os.path.join(CONFIG_DIR, "config.json")
AWS_DIR = os.path.expanduser("~/.aws")

# Regionen, in denen SES E-Mails empfangen kann
SES_INBOUND_REGIONS = [
    ("eu-central-1", "Europa (Frankfurt)"), ("eu-west-1", "Europa (Irland)"),
    ("eu-west-2", "Europa (London)"), ("eu-west-3", "Europa (Paris)"),
    ("eu-north-1", "Europa (Stockholm)"), ("eu-south-1", "Europa (Mailand)"),
    ("us-east-1", "USA Ost (N. Virginia)"), ("us-east-2", "USA Ost (Ohio)"),
    ("us-west-1", "USA West (N. Kalifornien)"), ("us-west-2", "USA West (Oregon)"),
    ("ca-central-1", "Kanada (Zentral)"), ("sa-east-1", "Südamerika (São Paulo)"),
    ("ap-northeast-1", "Asien-Pazifik (Tokio)"), ("ap-northeast-2", "Asien-Pazifik (Seoul)"),
    ("ap-southeast-1", "Asien-Pazifik (Singapur)"), ("ap-southeast-2", "Asien-Pazifik (Sydney)"),
    ("ap-south-1", "Asien-Pazifik (Mumbai)"), ("il-central-1", "Israel (Tel Aviv)"),
    ("af-south-1", "Afrika (Kapstadt)"), ("me-south-1", "Naher Osten (Bahrain)"),
]


# --------------------------------------------------------------------------- #
# Konfigurationsdatei
# --------------------------------------------------------------------------- #
DEFAULTS = {
    "profile": "", "region": "eu-central-1", "bucket": "", "prefix": "mail/",
    "from": "", "allow_delete": True, "port": 8765, "host": "127.0.0.1",
}


def load_config() -> dict:
    cfg = dict(DEFAULTS)
    try:
        with open(CONFIG_FILE, "r", encoding="utf-8") as fh:
            data = json.load(fh)
        if isinstance(data, dict):
            cfg.update({k: v for k, v in data.items() if k in DEFAULTS})
    except (OSError, ValueError):
        pass
    return cfg


def save_config(cfg: dict) -> str:
    os.makedirs(CONFIG_DIR, exist_ok=True)
    clean = {k: cfg.get(k, v) for k, v in DEFAULTS.items()}
    clean["prefix"] = normalize_prefix(clean["prefix"])
    if not clean["bucket"]:
        raise ValueError("Ohne Bucket geht es nicht")
    with open(CONFIG_FILE, "w", encoding="utf-8") as fh:
        json.dump(clean, fh, indent=2, ensure_ascii=False)
    os.chmod(CONFIG_FILE, stat.S_IRUSR | stat.S_IWUSR)
    return CONFIG_FILE


def config_exists() -> bool:
    return bool(load_config()["bucket"])


def normalize_prefix(prefix: str) -> str:
    prefix = (prefix or "").strip().lstrip("/")
    if prefix and not prefix.endswith("/"):
        prefix += "/"
    return prefix


# --------------------------------------------------------------------------- #
# AWS-Profile
# --------------------------------------------------------------------------- #
def aws_profiles() -> list[str]:
    names = set()
    for fname, prefix in ((os.path.join(AWS_DIR, "credentials"), ""),
                          (os.path.join(AWS_DIR, "config"), "profile ")):
        cp = configparser.ConfigParser()
        try:
            cp.read(fname)
        except configparser.Error:
            continue
        for sec in cp.sections():
            names.add(sec[len(prefix):] if prefix and sec.startswith(prefix) else sec)
    return sorted(n for n in names if n)


def write_credentials(profile: str, key_id: str, secret: str, region: str) -> str:
    """Zugangsdaten als benanntes Profil nach ~/.aws/credentials schreiben."""
    profile = (profile or "").strip() or "s3mail"
    key_id, secret = (key_id or "").strip(), (secret or "").strip()
    if not key_id or not secret:
        raise ValueError("Access Key ID und Secret Access Key werden beide gebraucht")
    if not key_id.startswith(("AKIA", "ASIA")) or len(key_id) < 16:
        raise ValueError("Das sieht nicht nach einer Access Key ID aus (beginnt mit AKIA…)")
    os.makedirs(AWS_DIR, exist_ok=True)

    cred_file = os.path.join(AWS_DIR, "credentials")
    cp = configparser.ConfigParser()
    cp.read(cred_file)
    if not cp.has_section(profile):
        cp.add_section(profile)
    cp.set(profile, "aws_access_key_id", key_id)
    cp.set(profile, "aws_secret_access_key", secret)
    with open(cred_file, "w", encoding="utf-8") as fh:
        cp.write(fh)
    os.chmod(cred_file, stat.S_IRUSR | stat.S_IWUSR)

    conf_file = os.path.join(AWS_DIR, "config")
    cp2 = configparser.ConfigParser()
    cp2.read(conf_file)
    sec = "default" if profile == "default" else f"profile {profile}"
    if not cp2.has_section(sec):
        cp2.add_section(sec)
    cp2.set(sec, "region", region or "eu-central-1")
    with open(conf_file, "w", encoding="utf-8") as fh:
        cp2.write(fh)
    os.chmod(conf_file, stat.S_IRUSR | stat.S_IWUSR)
    return profile


def make_session(profile: str | None, region: str | None):
    import boto3
    return boto3.Session(profile_name=profile or None, region_name=region or None)


# --------------------------------------------------------------------------- #
# Verbindungstest
# --------------------------------------------------------------------------- #
def _check(name, ok, detail="", hint="", skipped=False):
    return {"name": name, "ok": bool(ok), "detail": detail, "hint": hint, "skipped": skipped}


def list_buckets(session) -> list[str]:
    return [b["Name"] for b in session.client("s3").list_buckets().get("Buckets", [])]


def ses_identities(session) -> list[str]:
    try:
        ses = session.client("ses")
        ids = ses.list_identities(IdentityType="EmailAddress").get("Identities", [])
        attrs = ses.get_identity_verification_attributes(Identities=ids) \
                   .get("VerificationAttributes", {}) if ids else {}
        return sorted(i for i in ids
                      if attrs.get(i, {}).get("VerificationStatus") == "Success")
    except Exception:
        return []


def test_connection(session, bucket: str, prefix: str, sender: str = "",
                    probe_write: bool = True) -> list[dict]:
    """Prueft der Reihe nach, was s3mail braucht - jeder Punkt einzeln."""
    checks = []
    prefix = normalize_prefix(prefix)
    s3 = session.client("s3")

    # 1. Auflisten
    sample = []
    try:
        resp = s3.list_objects_v2(Bucket=bucket, Prefix=prefix, MaxKeys=25)
        sample = [o for o in resp.get("Contents", []) if o["Size"] > 0]
        n = resp.get("KeyCount", 0)
        checks.append(_check(
            "Bucket lesen", True,
            f"{n} Objekt(e) unter „{prefix or '/'}“ gefunden" if n
            else f"Zugriff klappt, aber unter „{prefix or '/'}“ liegt noch nichts",
            "" if n else "Sobald SES die erste Mail ablegt, taucht sie hier auf."))
    except ClientError as exc:
        code = exc.response.get("Error", {}).get("Code", "")
        checks.append(_check("Bucket lesen", False, code,
                             "Fehlt s3:ListBucket, oder Bucket/Region stimmen nicht."))
        return checks
    except Exception as exc:
        checks.append(_check("Bucket lesen", False, str(exc)))
        return checks

    # 2. Eine Mail wirklich lesen
    if sample:
        try:
            s3.get_object(Bucket=bucket, Key=sample[0]["Key"], Range="bytes=0-2047")
            checks.append(_check("Mail lesen", True, os.path.basename(sample[0]["Key"])))
        except Exception as exc:
            checks.append(_check("Mail lesen", False, str(exc), "s3:GetObject fehlt."))
    else:
        checks.append(_check("Mail lesen", True, "übersprungen – noch keine Mail da",
                             skipped=True))

    # 2b. Verschluesselung: serverseitig merkt man beim Lesen nicht, client-seitig schon
    if sample:
        checks.append(_encryption_check(session, s3, bucket, sample[0]["Key"]))

    # 3./4. Schreiben und Loeschen mit einem Wegwerf-Objekt
    if probe_write:
        probe = f"{prefix}.s3mail-probe"
        wrote = False
        try:
            s3.put_object(Bucket=bucket, Key=probe, Body=b"s3mail probe",
                          ContentType="text/plain")
            wrote = True
            checks.append(_check("Schreiben", True, "Testobjekt angelegt"))
        except Exception as exc:
            checks.append(_check(
                "Schreiben", False, str(exc),
                "s3:PutObject fehlt. Ohne Schreibrecht gibt es keine Ordner, "
                "keine Tags und keinen Papierkorb."))
        if wrote:
            try:
                s3.delete_object(Bucket=bucket, Key=probe)
                checks.append(_check("Löschen", True, "Testobjekt wieder entfernt"))
            except Exception as exc:
                checks.append(_check(
                    "Löschen", False, str(exc),
                    "s3:DeleteObject fehlt. Starte mit „Löschen sperren“, "
                    "dann bleibt alles beim Lesen."))

    # 5. Conditional Write (fuer gefahrloses Arbeiten von mehreren Rechnern)
    try:
        s3.put_object(Bucket=bucket, Key=f"{prefix}.s3mail-probe2", Body=b"x",
                      IfNoneMatch="*")
        s3.delete_object(Bucket=bucket, Key=f"{prefix}.s3mail-probe2")
        checks.append(_check("Conditional Writes", True,
                             "unterstützt – mehrere Rechner überschreiben sich nicht"))
    except ClientError as exc:
        code = exc.response.get("Error", {}).get("Code", "")
        if code in ("PreconditionFailed", "ConditionalRequestConflict"):
            checks.append(_check("Conditional Writes", True, "unterstützt"))
        else:
            checks.append(_check("Conditional Writes", False, code,
                                 "Kein Beinbruch: s3mail schreibt dann ohne Sperre.",
                                 skipped=True))
    except Exception as exc:
        checks.append(_check("Conditional Writes", False, str(exc), skipped=True))

    # 6. SES-Absender
    if sender:
        try:
            ses = session.client("ses")
            domain = sender.split("@")[-1]
            attrs = ses.get_identity_verification_attributes(
                Identities=[sender, domain]).get("VerificationAttributes", {})
            good = [i for i in (sender, domain)
                    if attrs.get(i, {}).get("VerificationStatus") == "Success"]
            checks.append(_check(
                "SES-Absender", bool(good),
                f"verifiziert: {', '.join(good)}" if good else f"{sender} ist nicht verifiziert",
                "" if good else "In der SES-Konsole die Adresse oder die Domain verifizieren."))
        except Exception as exc:
            checks.append(_check("SES-Absender", False, str(exc),
                                 "ses:GetIdentityVerificationAttributes fehlt.", skipped=True))
    return checks


def _encryption_check(session, s3, bucket: str, key: str) -> dict:
    try:
        head = s3.head_object(Bucket=bucket, Key=key)
    except Exception as exc:
        return _check("Verschlüsselung", False, str(exc), skipped=True)
    meta = head.get("Metadata") or {}

    if is_encrypted_envelope(meta):
        detail = "SES verschlüsselt client-seitig mit KMS"
        if not HAVE_CRYPTO:
            return _check("Verschlüsselung", False, detail,
                          "Zum Entschlüsseln fehlt ein Paket: pip install cryptography")
        try:
            body = s3.get_object(Bucket=bucket, Key=key)["Body"].read()
            decrypt_envelope(body, meta, session.client("kms"))
            return _check("Verschlüsselung", True, detail + " – Entschlüsseln klappt")
        except Exception as exc:
            return _check("Verschlüsselung", False, f"{detail} – {exc}",
                          "Vermutlich fehlt kms:Decrypt auf dem Schlüssel aus der SES-Regel.")

    sse = head.get("ServerSideEncryption")
    if sse == "aws:kms":
        return _check("Verschlüsselung", True,
                      "serverseitig mit KMS (SSE-KMS) – Lesen klappt bereits")
    if sse:
        return _check("Verschlüsselung", True, f"serverseitig ({sse})")
    return _check("Verschlüsselung", True, "keine – die Mails liegen im Klartext",
                  "Optional: im Bucket die Standardverschlüsselung einschalten.")


def set_trash_lifecycle(session, bucket: str, prefix: str, days: int) -> str:
    """Regel anlegen/aktualisieren, die den Papierkorb nach X Tagen leert."""
    days = int(days)
    s3 = session.client("s3")
    rule_id = "s3mail-trash"
    target = f"{normalize_prefix(prefix)}{TRASH}/"
    try:
        existing = s3.get_bucket_lifecycle_configuration(Bucket=bucket).get("Rules", [])
    except ClientError as exc:
        if exc.response.get("Error", {}).get("Code") != "NoSuchLifecycleConfiguration":
            raise
        existing = []
    rules = [r for r in existing if r.get("ID") != rule_id]
    if days > 0:
        rules.append({"ID": rule_id, "Status": "Enabled",
                      "Filter": {"Prefix": target},
                      "Expiration": {"Days": days}})
    if rules:
        s3.put_bucket_lifecycle_configuration(Bucket=bucket,
                                              LifecycleConfiguration={"Rules": rules})
    else:
        s3.delete_bucket_lifecycle(Bucket=bucket)
    return (f"Papierkorb ({target}) wird nach {days} Tagen automatisch geleert"
            if days > 0 else "Automatisches Leeren abgeschaltet")


def current_lifecycle_days(session, bucket: str) -> int:
    try:
        rules = session.client("s3").get_bucket_lifecycle_configuration(
            Bucket=bucket).get("Rules", [])
    except Exception:
        return 0
    for r in rules:
        if r.get("ID") == "s3mail-trash":
            return int(r.get("Expiration", {}).get("Days", 0))
    return 0


# --------------------------------------------------------------------------- #
# API des Assistenten
# --------------------------------------------------------------------------- #
def setup_api(path: str, data: dict) -> dict:
    """Alle /api/setup/*-Aufrufe. Wirft ValueError/ClientError bei Problemen."""
    cfg = load_config()
    profile = data.get("profile") or cfg["profile"]
    region = data.get("region") or cfg["region"]

    if path == "/api/setup/info":
        return {"config": cfg, "profiles": aws_profiles(),
                "regions": [{"id": r, "label": f"{lab} · {r}"} for r, lab in SES_INBOUND_REGIONS],
                "has_env_keys": bool(os.environ.get("AWS_ACCESS_KEY_ID")),
                "config_file": CONFIG_FILE}

    if path == "/api/setup/credentials":
        name = write_credentials(data.get("new_profile"), data.get("key_id"),
                                 data.get("secret"), region)
        return {"profile": name, "profiles": aws_profiles()}

    if path == "/api/setup/buckets":
        session = make_session(profile, region)
        try:
            return {"buckets": list_buckets(session), "identities": ses_identities(session)}
        except ClientError as exc:
            code = exc.response.get("Error", {}).get("Code", "")
            if code in ("AccessDenied", "AccessDeniedException"):
                return {"buckets": [], "identities": ses_identities(session),
                        "note": "Kein Recht zum Auflisten aller Buckets "
                                "(s3:ListAllMyBuckets) – Namen bitte direkt eintippen."}
            raise

    if path == "/api/setup/test":
        session = make_session(profile, region)
        bucket = (data.get("bucket") or "").strip()
        if not bucket:
            raise ValueError("Bitte einen Bucket angeben")
        checks = test_connection(session, bucket, data.get("prefix", ""),
                                 data.get("from", ""))
        return {"checks": checks,
                "lifecycle_days": current_lifecycle_days(session, bucket),
                "ok": all(c["ok"] or c["skipped"] for c in checks)}

    if path == "/api/setup/lifecycle":
        session = make_session(profile, region)
        return {"message": set_trash_lifecycle(session, data["bucket"],
                                               data.get("prefix", ""), data.get("days", 30))}

    if path == "/api/setup/save":
        valid_folder("")  # nur damit klar ist, dass Prefix != Ordner
        cfg.update({
            "profile": profile, "region": region,
            "bucket": (data.get("bucket") or "").strip(),
            "prefix": normalize_prefix(data.get("prefix", "")),
            "from": (data.get("from") or "").strip(),
            "allow_delete": bool(data.get("allow_delete", True)),
        })
        return {"config": cfg, "path": save_config(cfg)}

    raise KeyError(path)


# --------------------------------------------------------------------------- #
# Seite des Assistenten
# --------------------------------------------------------------------------- #
SETUP_PAGE = r"""<!doctype html>
<html lang="de">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>s3mail einrichten</title>
<style>
  :root { --bg:#0f1216; --panel:#161b22; --panel2:#1c232c; --line:#2a323d;
          --text:#e6edf3; --muted:#8b98a5; --accent:#4c8dff; --ok:#38b48b; --danger:#f2596b; }
  @media (prefers-color-scheme: light) {
    :root { --bg:#f5f7fa; --panel:#fff; --panel2:#eef2f7; --line:#dce3ec;
            --text:#16202b; --muted:#5d6b7a; } }
  * { box-sizing:border-box; }
  body { margin:0; padding:32px 16px 60px; background:var(--bg); color:var(--text);
         font:14.5px/1.6 -apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,sans-serif; }
  .wrap { max-width:660px; margin:0 auto; }
  h1 { font-size:22px; margin:0 0 4px; }
  .sub { color:var(--muted); margin-bottom:22px; }
  .card { background:var(--panel); border:1px solid var(--line); border-radius:14px;
          padding:18px 20px; margin-bottom:14px; }
  .card h2 { font-size:15px; margin:0 0 4px; display:flex; gap:8px; align-items:center; }
  .card h2 .num { width:22px; height:22px; border-radius:50%; background:var(--panel2);
                  color:var(--muted); font-size:12px; display:grid; place-items:center; }
  .hint { color:var(--muted); font-size:12.5px; margin:2px 0 12px; }
  label { display:block; font-size:12px; color:var(--muted); margin:10px 0 3px; }
  input, select { width:100%; padding:8px 10px; border-radius:8px; font:inherit;
                  border:1px solid var(--line); background:var(--panel2); color:var(--text); }
  input:focus, select:focus { outline:2px solid var(--accent); outline-offset:-1px; }
  .row { display:flex; gap:10px; } .row > * { flex:1; }
  button { font:inherit; background:var(--panel2); color:var(--text); cursor:pointer;
           border:1px solid var(--line); border-radius:8px; padding:8px 14px; }
  button:hover:not(:disabled) { border-color:var(--accent); }
  button:disabled { opacity:.5; cursor:default; }
  button.primary { background:var(--accent); border-color:var(--accent); color:#fff; }
  .bar { display:flex; gap:8px; align-items:center; flex-wrap:wrap; margin-top:14px; }
  .tabs { display:flex; gap:6px; margin-bottom:6px; }
  .tabs button.on { border-color:var(--accent); color:var(--accent); }
  .check { display:flex; gap:9px; padding:6px 0; border-bottom:1px solid var(--line);
           align-items:flex-start; }
  .check:last-child { border-bottom:none; }
  .mark { width:18px; flex:none; text-align:center; }
  .ok { color:var(--ok); } .bad { color:var(--danger); } .skip { color:var(--muted); }
  .check .d { color:var(--muted); font-size:12.5px; }
  .msg { margin-top:10px; font-size:13px; }
  code { background:var(--panel2); padding:1px 5px; border-radius:5px; font-size:12.5px; }
  .foot { color:var(--muted); font-size:12px; text-align:center; margin-top:20px; }
</style>
</head>
<body><div class="wrap">
  <h1>s3mail einrichten</h1>
  <div class="sub">Einmal ausfüllen – danach startet s3mail direkt ins Postfach.</div>

  <div class="card">
    <h2><span class="num">1</span> AWS-Zugang</h2>
    <div class="hint">s3mail braucht einen Zugang zu deinem AWS-Konto, um die Mails aus
      S3 zu holen. Wenn auf diesem Rechner schon ein Profil eingerichtet ist, nimm das.</div>
    <div class="tabs">
      <button id="tabExisting" class="on">Vorhandenes Profil</button>
      <button id="tabNew">Neue Zugangsdaten</button>
    </div>
    <div id="paneExisting">
      <label>Profil</label>
      <select id="profile"></select>
    </div>
    <div id="paneNew" style="display:none">
      <label>Name für das neue Profil</label>
      <input id="new_profile" value="s3mail" placeholder="s3mail">
      <label>Access Key ID</label>
      <input id="key_id" placeholder="AKIA…" autocomplete="off" spellcheck="false">
      <label>Secret Access Key</label>
      <input id="secret" type="password" autocomplete="off" spellcheck="false">
      <div class="hint">Wird als AWS-Profil in <code>~/.aws/credentials</code> gespeichert
        (nur für dich lesbar) – dem Standardort, den auch die AWS-Werkzeuge nutzen.</div>
      <button id="saveCreds">Zugangsdaten speichern</button>
      <div id="credMsg" class="msg"></div>
    </div>
    <label>Region</label>
    <select id="region"></select>
    <div class="hint">Die Region, in der dein Bucket liegt und SES die Mails annimmt.</div>
  </div>

  <div class="card">
    <h2><span class="num">2</span> Postfach</h2>
    <div class="hint">Der Bucket, in den deine SES-Regel die Mails schreibt.</div>
    <label>Bucket</label>
    <div class="row">
      <select id="bucketSel"><option value="">– Buckets laden –</option></select>
      <button id="loadBuckets" style="flex:none">Buckets laden</button>
    </div>
    <input id="bucket" placeholder="oder Bucket-Namen hier eintippen" style="margin-top:8px">
    <label>Ordner im Bucket (Prefix)</label>
    <input id="prefix" placeholder="mail/">
    <div class="hint">Genau das Prefix aus deiner SES-Regel. Darunter legt s3mail
      <code>spam/</code>, <code>trash/</code> und <code>archiv/</code> an.</div>
    <label>Absender für Antworten</label>
    <div class="row">
      <select id="identSel"><option value="">– verifizierte Adressen –</option></select>
    </div>
    <input id="from" placeholder="oder Adresse eintippen – muss in SES verifiziert sein" style="margin-top:8px">
    <label style="display:flex;gap:8px;align-items:center;margin-top:14px">
      <input type="checkbox" id="allow_delete" checked style="width:auto">
      <span style="color:var(--text)">Endgültiges Löschen erlauben (aus dem Papierkorb heraus)</span>
    </label>
  </div>

  <div class="card">
    <h2><span class="num">3</span> Prüfen</h2>
    <div class="hint">Legt kurz ein Testobjekt an und löscht es wieder – so siehst du
      sofort, ob alle Rechte da sind.</div>
    <button class="primary" id="test">Verbindung testen</button>
    <div id="checks" style="margin-top:12px"></div>
    <div id="lifeBox" style="display:none;margin-top:14px">
      <label>Papierkorb automatisch leeren nach</label>
      <div class="row">
        <select id="days">
          <option value="0">gar nicht</option>
          <option value="7">7 Tagen</option>
          <option value="30" selected>30 Tagen</option>
          <option value="90">90 Tagen</option>
        </select>
        <button id="setLife" style="flex:none">Regel setzen</button>
      </div>
      <div class="hint">Setzt eine S3-Lifecycle-Regel auf <code>…/trash/</code>.
        AWS räumt dann selbst auf, ohne dass s3mail laufen muss.</div>
      <div id="lifeMsg" class="msg"></div>
    </div>
  </div>

  <div class="bar">
    <button class="primary" id="save" style="padding:10px 20px">Speichern und starten</button>
    <span id="saveMsg" class="msg"></span>
  </div>
  <div class="foot" id="foot"></div>
</div>

<script>
const $ = s => document.querySelector(s);
let info = {};

const api = async (path, body) => {
  const r = await fetch(path, {method:"POST", headers:{"Content-Type":"application/json"},
                               body: JSON.stringify(body || {})});
  const d = await r.json().catch(() => ({error:"Antwort nicht lesbar"}));
  if (!r.ok || d.error) throw new Error(d.error || ("HTTP " + r.status));
  return d;
};
const say = (el, text, bad) => {
  $(el).innerHTML = `<span class="${bad ? "bad" : "ok"}">${bad ? "✕" : "✓"}</span> ` +
    text.replace(/[<>]/g, "");
};
const collect = () => ({
  profile: $("#profile").value, region: $("#region").value,
  bucket: $("#bucket").value.trim(), prefix: $("#prefix").value.trim(),
  from: $("#from").value.trim(), allow_delete: $("#allow_delete").checked,
});

async function boot() {
  info = await api("/api/setup/info");
  const c = info.config;
  $("#region").innerHTML = info.regions.map(r =>
    `<option value="${r.id}"${r.id === c.region ? " selected" : ""}>${r.label}</option>`).join("");
  fillProfiles(c.profile);
  $("#bucket").value = c.bucket; $("#prefix").value = c.prefix || "mail/";
  $("#from").value = c.from; $("#allow_delete").checked = c.allow_delete !== false;
  $("#foot").textContent = "Wird gespeichert in " + info.config_file;
  if (!info.profiles.length && !info.has_env_keys) $("#tabNew").click();
}
function fillProfiles(sel) {
  const opts = info.profiles.map(p =>
    `<option value="${p}"${p === sel ? " selected" : ""}>${p}</option>`).join("");
  $("#profile").innerHTML = (info.has_env_keys || !info.profiles.length
      ? `<option value="">(Umgebungsvariablen / Standard)</option>` : "") + opts;
}

$("#tabExisting").onclick = () => {
  $("#tabExisting").classList.add("on"); $("#tabNew").classList.remove("on");
  $("#paneExisting").style.display = ""; $("#paneNew").style.display = "none";
};
$("#tabNew").onclick = () => {
  $("#tabNew").classList.add("on"); $("#tabExisting").classList.remove("on");
  $("#paneNew").style.display = ""; $("#paneExisting").style.display = "none";
};

$("#saveCreds").onclick = async () => {
  try {
    const d = await api("/api/setup/credentials", {
      new_profile: $("#new_profile").value, key_id: $("#key_id").value,
      secret: $("#secret").value, region: $("#region").value});
    info.profiles = d.profiles;
    fillProfiles(d.profile);
    $("#secret").value = "";
    $("#tabExisting").click();
    say("#credMsg", "Profil „" + d.profile + "“ gespeichert.");
  } catch (e) { say("#credMsg", e.message, true); }
};

$("#loadBuckets").onclick = async () => {
  const b = $("#loadBuckets"); b.disabled = true; b.textContent = "lädt…";
  try {
    const d = await api("/api/setup/buckets", collect());
    $("#bucketSel").innerHTML = '<option value="">– auswählen –</option>' +
      d.buckets.map(x => `<option${x === $("#bucket").value ? " selected" : ""}>${x}</option>`).join("");
    $("#identSel").innerHTML = '<option value="">– verifizierte Adressen –</option>' +
      d.identities.map(x => `<option>${x}</option>`).join("");
    if (d.note) say("#saveMsg", d.note, true);
  } catch (e) { say("#saveMsg", e.message, true); }
  finally { b.disabled = false; b.textContent = "Buckets laden"; }
};
$("#bucketSel").onchange = e => { if (e.target.value) $("#bucket").value = e.target.value; };
$("#identSel").onchange = e => { if (e.target.value) $("#from").value = e.target.value; };

$("#test").onclick = async () => {
  const b = $("#test"); b.disabled = true; b.textContent = "prüft…";
  $("#checks").innerHTML = "";
  try {
    const d = await api("/api/setup/test", collect());
    $("#checks").innerHTML = d.checks.map(c => `
      <div class="check">
        <span class="mark ${c.skipped ? "skip" : c.ok ? "ok" : "bad"}">${c.skipped ? "–" : c.ok ? "✓" : "✕"}</span>
        <div><b>${c.name}</b>${c.detail ? ' <span class="d">· ' + c.detail + "</span>" : ""}
        ${c.hint ? '<div class="d">' + c.hint + "</div>" : ""}</div>
      </div>`).join("");
    $("#lifeBox").style.display = "";
    if (d.lifecycle_days) $("#days").value = String(d.lifecycle_days);
  } catch (e) { say("#checks", e.message, true); }
  finally { b.disabled = false; b.textContent = "Verbindung testen"; }
};

$("#setLife").onclick = async () => {
  try {
    const d = await api("/api/setup/lifecycle", {...collect(), days: +$("#days").value});
    say("#lifeMsg", d.message);
  } catch (e) { say("#lifeMsg", e.message, true); }
};

$("#save").onclick = async () => {
  const b = $("#save"); b.disabled = true; b.textContent = "speichert…";
  try {
    await api("/api/setup/save", collect());
    say("#saveMsg", "Gespeichert – Postfach wird geladen…");
    setTimeout(() => location.href = "/", 700);
  } catch (e) {
    say("#saveMsg", e.message, true);
    b.disabled = false; b.textContent = "Speichern und starten";
  }
};

boot().catch(e => say("#saveMsg", e.message, true));
</script>
</body>
</html>
"""


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

    # -- GET ---------------------------------------------------------------- #
    def do_GET(self):
        url = urlparse(self.path)
        qs = parse_qs(url.query, keep_blank_values=True)

        def run():
            if url.path in ("/", "/index.html"):
                if self.store is None:                 # noch nicht eingerichtet
                    return self._send(200, SETUP_PAGE.encode("utf-8"),
                                      "text/html; charset=utf-8")
                page = PAGE.replace("__CONFIG__", json.dumps(self.config))
                self._send(200, page.encode("utf-8"), "text/html; charset=utf-8")
            elif url.path in ("/setup", "/setup/"):
                self._send(200, SETUP_PAGE.encode("utf-8"), "text/html; charset=utf-8")
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

    httpd = ThreadingHTTPServer((cfg["host"], int(cfg["port"])), Handler)
    url = f"http://{cfg['host']}:{cfg['port']}/"
    if store is None:
        print(f"s3mail ist noch nicht eingerichtet - Assistent: {url}")
    else:
        print(f"s3mail laeuft auf {url}   (Strg+C zum Beenden)")
        print(f"Bucket: {cfg['bucket']}/{store.root}   Cache: {store.cache_file}")
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
