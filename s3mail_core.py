#!/usr/bin/env python3
"""
s3mail core - Index, Zustand (Tags/Gelesen/Stern), Ordner, Regeln, Versand.

Wird von s3mail.py importiert; enthaelt keine UI.
"""

from __future__ import annotations

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
