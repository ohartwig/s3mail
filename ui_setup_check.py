import os, json, sys, tempfile, threading
from http.server import ThreadingHTTPServer
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import re
import s3mail

TOKEN = "ui-test-token"
OUT = os.environ.get("S3MAIL_UI_OUT", os.path.dirname(os.path.abspath(__file__)))
from test_s3mail import build_mails, FakeS3, FakeSession, FakeSESIdentities

for var in ("AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_PROFILE"):
    os.environ.pop(var, None)   # frischer Rechner ohne AWS-Setup simulieren

home = tempfile.mkdtemp()
s3mail.CONFIG_DIR = os.path.join(home, ".config", "s3mail")
s3mail.CONFIG_FILE = os.path.join(s3mail.CONFIG_DIR, "config.json")
s3mail.AWS_DIR = os.path.join(home, ".aws")
s3 = FakeS3(build_mails())
s3mail.make_session = lambda p, r: FakeSession(s3)
s3mail.Handler.store = None; s3mail.Handler.sender = None

s3mail.Handler.token = TOKEN
s3mail.Handler.bind, s3mail.Handler.port = "127.0.0.1", 8803
httpd = ThreadingHTTPServer(("127.0.0.1", 8803), s3mail.Handler)
threading.Thread(target=httpd.serve_forever, daemon=True).start()

from playwright.sync_api import sync_playwright
errs = []
with sync_playwright() as p:
    b = p.chromium.launch()
    pg = b.new_page(viewport={"width":900,"height":1250}, color_scheme="dark")
    pg.on("pageerror", lambda e: errs.append("pageerror: " + str(e)))
    pg.on("console", lambda m: errs.append("console: " + m.text) if m.type == "error" else None)
    pg.goto("http://127.0.0.1:8803/?t=" + TOKEN)
    pg.wait_for_function("document.querySelectorAll('#region option').length > 0")

    # Ohne vorhandenes Profil sollte direkt der "Neue Zugangsdaten"-Tab offen sein
    assert pg.is_visible("#paneNew"), "ohne Profil nicht auf den Key-Tab gesprungen"
    print("Start: Key-Eingabe offen, Regionen geladen:", pg.locator("#region option").count())

    pg.fill("#key_id", "AKIAIOSFODNN7EXAMPLE")
    pg.fill("#secret", "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY")
    pg.click("#saveCreds"); pg.wait_for_timeout(500)
    print("Credentials:", pg.inner_text("#credMsg"))
    assert "gespeichert" in pg.inner_text("#credMsg")
    assert os.path.exists(os.path.join(home, ".aws", "credentials"))
    assert pg.is_visible("#paneExisting"), "nach dem Speichern nicht zurueckgewechselt"
    assert pg.input_value("#secret") == "", "secret blieb im Feld stehen"

    pg.click("#loadBuckets"); pg.wait_for_timeout(500)
    pg.select_option("#bucketSel", "test-bucket")
    pg.select_option("#identSel", "support@firma.de")
    pg.fill("#prefix", "mail/")
    assert pg.input_value("#bucket") == "test-bucket" and pg.input_value("#from") == "support@firma.de"
    print("Bucket/Absender aus den Dropdowns übernommen")

    pg.click("#test"); pg.wait_for_selector(".check")
    pg.wait_for_timeout(400)
    checks = pg.inner_text("#checks")
    print("Checks:", " | ".join(l for l in checks.splitlines() if l.strip())[:300])
    assert "Bucket lesen" in checks and "Conditional Writes" in checks
    assert pg.is_visible("#lifeBox"), "Lifecycle-Block nicht aufgetaucht"
    pg.screenshot(path=os.path.join(OUT, "v3-setup.png"), full_page=True)

    pg.select_option("#days", "30"); pg.click("#setLife"); pg.wait_for_timeout(400)
    print("Lifecycle:", pg.inner_text("#lifeMsg"))
    assert any(r["ID"] == "s3mail-trash" for r in (s3.lifecycle or [])), "keine Regel gesetzt"

    pg.click("#save")
    pg.wait_for_url(re.compile(r"127\.0\.0\.1:8803/(\?|$)"), timeout=5000)
    pg.wait_for_selector(".item", timeout=5000)
    print("Nach dem Speichern:", pg.inner_text("#box"), "|", pg.locator(".item").count(), "Mails")
    cfg = json.load(open(s3mail.CONFIG_FILE))
    assert cfg["bucket"] == "test-bucket" and cfg["prefix"] == "mail/"
    assert oct(os.stat(s3mail.CONFIG_FILE).st_mode & 0o777) == "0o600"
    pg.screenshot(path=os.path.join(OUT, "v3-after-setup.png"))

    # Einstellungen-Knopf führt zurück in den Assistenten, jetzt vorbefüllt
    pg.click("text=Einstellungen"); pg.wait_for_selector("#bucket")
    pg.wait_for_timeout(400)
    assert pg.input_value("#bucket") == "test-bucket", pg.input_value("#bucket")
    print("Einstellungen-Knopf: Assistent vorbefüllt mit", pg.input_value("#bucket"))
    b.close()
httpd.shutdown()
print("\nJS-Fehler:", errs or "keine")
assert not errs
print("SETUP-DURCHLAUF BESTANDEN")
