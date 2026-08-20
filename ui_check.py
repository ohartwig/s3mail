import sys, tempfile, threading
from http.server import ThreadingHTTPServer
sys.path.insert(0, "/home/claude")
import s3mail
from test_s3mail import build_mails, FakeS3, FakeSES

s3 = FakeS3(build_mails())
store = s3mail.MailStore(s3, "meine-mails", "mail/", cache_dir=tempfile.mkdtemp())
store.state.set_rules([{"contains": "rechnungen.de", "field": "from", "folder": "archiv",
                        "tags": ["Buchhaltung"]}])
store.refresh()
store.state.set_tags(["m1"], add=["wichtig", "Kunde"])
s3mail.Handler.store = store
s3mail.Handler.sender = s3mail.Sender(FakeSES(), "support@firma.de")
s3mail.Handler.config = {"bucket":"meine-mails","root":"mail/","default_from":"support@firma.de","can_send":True}
httpd = ThreadingHTTPServer(("127.0.0.1", 8801), s3mail.Handler)
threading.Thread(target=httpd.serve_forever, daemon=True).start()

from playwright.sync_api import sync_playwright
errs = []
with sync_playwright() as p:
    b = p.chromium.launch()
    pg = b.new_page(viewport={"width":1440,"height":860}, color_scheme="dark")
    pg.on("pageerror", lambda e: errs.append("pageerror: " + str(e)))
    pg.on("console", lambda m: errs.append("console: " + m.text) if m.type == "error" else None)
    pg.goto("http://127.0.0.1:8801/")
    pg.wait_for_selector(".item")

    # Sidebar-Zähler
    side = pg.inner_text("#side")
    assert "Posteingang" in side and "Archiv" in side and "wichtig" in side, side
    print("Sidebar:", " | ".join(x for x in side.splitlines() if x.strip())[:220])

    # Mail öffnen -> gelesen
    pg.click(".item")
    pg.wait_for_selector("#view h2")
    pg.wait_for_timeout(300)
    pg.screenshot(path="/home/claude/v2-read.png")

    # Mehrfachauswahl: zwei Checkboxen, zweite mit Shift
    pg.click('[data-chk="1"]'); pg.wait_for_timeout(120)
    pg.click('[data-chk="2"]', modifiers=["Shift"]); pg.wait_for_timeout(200)
    tools = pg.inner_text("#tools")
    assert "2" in tools and "gewählt" in tools, tools
    pg.screenshot(path="/home/claude/v2-bulk.png")
    print("Bulk-Leiste:", tools.replace("\n", " "))

    # Massen-Tag über Menü
    pg.click("#mTag"); pg.wait_for_selector(".menu")
    pg.fill("#mkTag", "Sammelaktion"); pg.press("#mkTag", "Enter")
    pg.wait_for_timeout(500)
    assert "Sammelaktion" in pg.inner_text("#side"), pg.inner_text("#side")
    print("Tag per Massenaktion angelegt")

    # Ordner wechseln: Archiv (dort liegt die per Regel einsortierte Mail)
    pg.click('[data-f="archiv"]'); pg.wait_for_timeout(400)
    arch = pg.inner_text("#list")
    assert "Ihre Rechnung Juli" in arch, arch
    assert "Buchhaltung" in arch, "Regel-Tag fehlt in der Liste"
    print("Archiv nach Regel:", arch.replace("\n", " · ")[:150])
    pg.screenshot(path="/home/claude/v2-archiv.png")

    # Papierkorb-Flow: Mail in den Trash, dann Papierkorb ansehen
    pg.click(".item"); pg.wait_for_selector("#vTrash"); pg.click("#vTrash")
    pg.wait_for_timeout(500)
    pg.click('[data-f="trash"]'); pg.wait_for_timeout(400)
    assert "Ihre Rechnung Juli" in pg.inner_text("#list")
    assert pg.query_selector("#emptyTrash"), "Papierkorb-leeren-Button fehlt"
    pg.screenshot(path="/home/claude/v2-trash.png")
    print("Papierkorb ok")

    # Endgültig löschen inkl. Bestätigungsdialog
    pg.click(".item"); pg.wait_for_selector("#vPurge"); pg.click("#vPurge")
    pg.wait_for_selector("dialog[open] #cf_ok")
    pg.screenshot(path="/home/claude/v2-confirm.png")
    print("Confirm:", pg.inner_text("#cf_text"))
    pg.click("#cf_ok"); pg.wait_for_timeout(600)
    assert "mail/trash/m3" not in s3.objs, "objekt nicht wirklich geloescht"
    assert "Nichts hier" in pg.inner_text("#list")
    print("Endgültig gelöscht, Objekt ist aus S3 weg")

    # Regel-Editor
    pg.click("#rulesBtn"); pg.wait_for_selector("dialog[open] #ruleRows")
    pg.wait_for_timeout(200)
    pg.screenshot(path="/home/claude/v2-rules.png")
    assert pg.input_value(".r_contains") == "rechnungen.de"
    pg.click("#ruleAdd")
    assert len(pg.query_selector_all(".rule .r_contains")) == 2
    pg.press("body", "Escape"); pg.wait_for_timeout(200)
    print("Regel-Editor ok")

    # Antworten-Dialog
    pg.click('[data-f=""]'); pg.wait_for_timeout(300)
    pg.click(".item"); pg.wait_for_selector("#reply"); pg.click("#reply")
    pg.wait_for_timeout(300)
    print("Reply an:", pg.input_value("#c_to"), "| Betreff:", pg.input_value("#c_subject"))
    pg.press("body", "Escape"); pg.wait_for_timeout(200)

    # Tastatur: j blättert, s markiert
    pg.press("body", "j"); pg.wait_for_timeout(300)
    pg.press("body", "s"); pg.wait_for_timeout(400)
    assert pg.query_selector(".star.on"), "Stern per Tastatur nicht gesetzt"
    print("Tastatur j/s ok")

    pg.click('[data-f=""]'); pg.wait_for_timeout(300)
    pg.screenshot(path="/home/claude/v2-inbox.png")
    pg_l = b.new_page(viewport={"width":1440,"height":860}, color_scheme="light")
    pg_l.on("pageerror", lambda e: errs.append("light pageerror: " + str(e)))
    pg_l.goto("http://127.0.0.1:8801/"); pg_l.wait_for_selector(".item")
    pg_l.click(".item"); pg_l.wait_for_selector("#view h2"); pg_l.wait_for_timeout(400)
    pg_l.screenshot(path="/home/claude/v2-light.png")
    b.close()
httpd.shutdown()
print("\nJS-Fehler:", errs or "keine")
assert not errs
print("UI-DURCHLAUF BESTANDEN")
