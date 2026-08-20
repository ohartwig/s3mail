#!/usr/bin/env python3
"""s3mail setup - Konfigurationsdatei und Einrichtungs-Assistent im Browser."""

from __future__ import annotations

import configparser
import json
import os
import stat

from s3mail_core import (ClientError, STATE_OBJECT, STATE_OPS, TRASH, valid_folder,
                         HAVE_CRYPTO, decrypt_envelope, is_encrypted_envelope)

# === BUILD:SPLIT ===

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

    # 5. Auflisten des Ops-Ordners (dort liegen die Zustandsaenderungen)
    try:
        s3.list_objects_v2(Bucket=bucket, Prefix=f"{prefix}{STATE_OPS}", MaxKeys=1)
        checks.append(_check("Zustand von mehreren Rechnern", True,
                             "Ordner für die Änderungen ist lesbar"))
    except ClientError as exc:
        code = exc.response.get("Error", {}).get("Code", "")
        checks.append(_check(
            "Zustand von mehreren Rechnern", False, code,
            f"s3:ListBucket auf {prefix}{STATE_OPS}* fehlt. Ohne das sieht dieser "
            "Rechner Änderungen der anderen erst nach dem nächsten Zusammenfassen."))
    except Exception as exc:
        checks.append(_check("Zustand von mehreren Rechnern", False, str(exc),
                             skipped=True))

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
