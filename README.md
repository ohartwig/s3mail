# s3mail

Mail-Client für E-Mails, die Amazon SES als Rohdaten (MIME) in einen S3-Bucket schreibt.
Ein einzelnes Programm ohne Laufzeitumgebung, startet einen lokalen Webserver,
Postfach im Browser.

## Voraussetzungen

### Auf dem Rechner, der s3mail startet

Nichts. Kein Webserver, keine Datenbank, kein Docker, keine Laufzeitumgebung –
s3mail ist ein einzelnes Programm, bringt seinen eigenen Server mit und bindet
ihn an 127.0.0.1.

Das passende Paket aus den
[Releases](https://git.ole-hartwig.eu/development/s3mail/-/releases) laden –
macOS (Apple Silicon und Intel), Linux und Windows, jeweils amd64/arm64:

```bash
unzip s3mail-macos-arm64.zip
open s3mail.app
```

Die macOS-Pakete sind signiert und notarisiert – kein `xattr`, kein Rechtsklick.
Unter Linux und Windows liegt im ZIP die nackte Datei, dort genügt ein
Doppelklick bzw. `./s3mail`.

Wer selbst bauen will, braucht Go 1.25 oder neuer – siehe
[Selbst bauen](#selbst-bauen).

Dazu ein Browser. Nach außen gehen nur HTTPS-Verbindungen zu
`s3.<region>.amazonaws.com`, `email.<region>.amazonaws.com` und – falls
verschlüsselt – `kms.<region>.amazonaws.com`.

### Im AWS-Konto

| Was | Wofür | Fehlt es, dann … |
|---|---|---|
| In SES verifizierte **Domain** | Empfang überhaupt | kommt keine Mail an |
| **MX-Record** auf SES | leitet die Mail zu AWS | Mail geht an den alten Server |
| **S3-Bucket** in derselben Region | die Mails liegen dort | – |
| **Bucket-Policy** für SES | SES darf hineinschreiben | Mails werden verworfen |
| Aktives **Receipt-Rule-Set** mit S3-Aktion | schreibt die Mail in den Bucket | Mail wird angenommen und weggeworfen |
| **IAM-Identität** mit der [Policy unten](#iam-policy) | s3mail liest und sortiert | s3mail kommt nicht an den Bucket |
| Verifizierte **Absenderadresse** | Schreiben, Antworten, Weiterleiten | Lesen geht, Senden nicht (`--no-send`) |
| **KMS-Schlüssel** (optional) | nur bei Verschlüsselung | siehe [Verschlüsselte Buckets](#verschlüsselte-buckets) |

Nicht gebraucht werden Lambda, EC2, VPC oder eine WorkMail-Organisation.

### Einrichten, der Reihe nach

1. **Region wählen.** SES nimmt Mail nur in bestimmten Regionen entgegen; der
   Assistent bietet genau diese an (Frankfurt, Irland, London, Paris, Stockholm,
   Mailand, die US-Regionen, Kanada, São Paulo, Tokio, Seoul, Singapur, Sydney,
   Mumbai, Tel Aviv, Kapstadt, Bahrain). Bucket und SES gehören in dieselbe Region.
2. **Domain in SES verifizieren** und die DKIM-CNAMEs im DNS setzen.
3. **MX-Record** der Domain auf `inbound-smtp.<region>.amazonaws.com`, Priorität 10.
4. **S3-Bucket anlegen**, darin ein Prefix als Wurzel, üblicherweise `mail/`.
5. **Bucket-Policy**, damit SES schreiben darf – die SES-Konsole bietet sie beim
   Anlegen der Regel an, von Hand sieht sie so aus:

   ```json
   {
     "Version": "2012-10-17",
     "Statement": [{
       "Sid": "AllowSESPuts",
       "Effect": "Allow",
       "Principal": {"Service": "ses.amazonaws.com"},
       "Action": "s3:PutObject",
       "Resource": "arn:aws:s3:::MEIN-BUCKET/mail/*",
       "Condition": {
         "StringEquals": {"aws:SourceAccount": "123456789012"},
         "StringLike": {
           "aws:SourceArn": "arn:aws:ses:REGION:123456789012:receipt-rule-set/*"}
       }
     }]
   }
   ```

6. **Receipt-Rule-Set** anlegen, darin eine Regel mit der Aktion **S3** → Bucket
   und Prefix `mail/`. Danach **das Rule-Set aktivieren** – ein angelegtes, aber
   inaktives Rule-Set ist der häufigste Grund dafür, dass nichts ankommt.
7. **Zugang für s3mail.** Drei Wege, je nachdem, wie euer AWS-Konto organisiert ist:

   | Weg | Was zu tun ist | AWS CLI nötig? |
   |---|---|---|
   | **IAM-Benutzer mit Access Key** *(empfohlen)* | In der IAM-Konsole einen Benutzer mit der [Policy unten](#iam-policy) anlegen, Access Key erzeugen, die beiden Werte in Schritt 1 des Assistenten eintragen | nein |
   | **Vorhandenes Profil mit Schlüsseln** | Im Assistenten aus der Liste wählen | nein |
   | **AWS SSO / IAM Identity Center** | Einmal `aws sso login --profile NAME`, dann das Profil im Assistenten wählen | ja, zum Anmelden |

   **IAM-Benutzer anlegen, Schritt für Schritt** (einmalig, ein paar Minuten):

   1. AWS-Konsole → **IAM** → *Benutzer* → **Benutzer erstellen**, Name z. B.
      `s3mail`. Zugriff auf die Konsole braucht er nicht.
   2. Berechtigungen → *Richtlinien direkt anfügen* → **Richtlinie erstellen** →
      Reiter **JSON** → die [Policy weiter unten](#iam-policy) einfügen und den
      Bucket-Namen anpassen.
   3. Beim fertigen Benutzer → **Sicherheitsanmeldeinformationen** →
      *Zugriffsschlüssel erstellen*, Anwendungsfall „Anwendung außerhalb von AWS".
      Das **Secret wird nur ein einziges Mal angezeigt** – jetzt kopieren.
   4. Im Assistenten Reiter **„Neue Zugangsdaten"**, beides einfügen, speichern.
      Die Bucket-Liste füllt sich danach von selbst.

   Der Assistent schreibt eingetippte Schlüssel als **eigenes, zusätzliches Profil**
   nach `~/.aws/credentials` (Standard-Name `s3mail`, chmod 600) und ergänzt in
   `~/.aws/config` nur `[profile s3mail]` mit der Region. Vorhandene Profile bleiben
   unangetastet – auch ein `default`, das sich ganz anders anmeldet. Die AWS CLI wird
   auf diesem Weg nirgends gebraucht; `~/.aws/credentials` ist bloß die Datei, an der
   alle AWS-Werkzeuge nachsehen.

   Was **nicht** funktioniert: ein Profil, das sich über das neue `aws login`
   anmeldet (Eintrag `login_session` in `~/.aws/config`). Das bräuchte
   `botocore[crt]`, das im fertigen Paket nicht enthalten ist. Der Assistent sagt
   es dir in dem Fall im Klartext und zeigt die Alternativen.
8. **Absenderadresse verifizieren**, wenn gesendet werden soll. Eine verifizierte
   *Domain* genügt – dann darf jede Adresse darunter senden, ohne einzeln
   freigeschaltet zu werden. Solange das
   SES-Konto in der **Sandbox** steckt, kann es außerdem nur an verifizierte
   Adressen senden – Empfang funktioniert in der Sandbox uneingeschränkt, das
   Antworten nach draußen nicht. Für den Produktionszugang bei AWS einen Antrag
   stellen oder mit `--no-send` im Lesemodus bleiben.
9. **Testmail schicken**, dann in s3mail „Neu laden".

Ob das alles sitzt, muss man nicht raten: Schritt 3 des Assistenten prüft der
Reihe nach Bucket lesen, Mail lesen, Schreiben, Löschen, Zustand von mehreren
Rechnern, Verschlüsselung und SES-Absender – und schreibt zu jedem fehlenden Punkt
die IAM-Aktion dazu, die dafür nötig wäre.

## Erster Start

```bash
./s3mail
```

Beim Start schreibt s3mail eine Adresse ins Terminal, die ein **Token** enthält:

```
s3mail laeuft auf http://127.0.0.1:8765/?t=8Kd2...   (Strg+C zum Beenden)
```

Der Browser wird damit von selbst geöffnet. Ohne dieses Token antwortet der Server
nicht – siehe [Wer darf ran](#wer-darf-ran).

Beim ersten Mal öffnet sich der **Einrichtungs-Assistent** im Browser statt des
Postfachs. Dort in drei Schritten:

1. **AWS-Zugang** – entweder ein Profil auswählen, das auf dem Rechner schon existiert,
   oder Access Key ID + Secret eintragen. Die Keys werden als benanntes AWS-Profil nach
   `~/.aws/credentials` geschrieben (chmod 600) – der Standardort, den auch AWS CLI und
   alle anderen AWS-Werkzeuge lesen. Dazu die Region.
2. **Postfach** – Bucket aus einer Liste wählen (oder eintippen), Prefix aus der
   SES-Regel, Absenderadresse für Antworten (Dropdown zeigt die in SES verifizierten
   Adressen). Dazu optional Signatur und ein Name für den Umschalter.
3. **Prüfen** – legt kurz ein Testobjekt an und löscht es wieder. Ergebnis ist eine
   Checkliste: Bucket lesen, Mail lesen, Schreiben, Löschen, Zustand von mehreren
   Rechnern, SES-Absender. Was fehlt, steht im Klartext dabei, inklusive der IAM-Aktion.
   Danach lässt sich optional eine Lifecycle-Regel setzen, die den Papierkorb nach
   7/30/90 Tagen automatisch leert.

„Speichern und starten“ schreibt `~/.config/s3mail/config.json` (chmod 600) und lädt
direkt das Postfach. Ab dann genügt `./s3mail`. Über den Knopf
**Einstellungen** oben rechts kommt man jederzeit zurück in den Assistenten – dort
lässt sich auch ein zweites Postfach anlegen, oder über **+ Postfach hinzufügen**
im Umschalter oben links.

Wer lieber Argumente tippt, kann alles weiterhin per CLI setzen – die überschreiben die
Konfigurationsdatei für den jeweiligen Start. Bei mehreren Postfächern wirken
`--bucket`, `--prefix`, `--region`, `--profile` und `--from` auf das **erste**;
ein Schalter kann nicht sagen, welches von mehreren er meint:

```bash
./s3mail --bucket mein-mail-bucket --prefix mail/ \
    --region eu-central-1 --from support@example.com
```

| Option | Bedeutung |
|---|---|
| `--setup` | Assistent öffnen, auch wenn schon konfiguriert |
| `--bucket` | S3-Bucket mit den Rohmails |
| `--prefix` | Wurzel-Prefix, z. B. `mail/` |
| `--region` / `--profile` | AWS-Region bzw. Profil |
| `--from` | Absenderadresse für Antworten (in SES verifiziert) |
| `--no-send` | SES-Versand deaktivieren (reiner Lesemodus) |
| `--no-delete` | Endgültiges Löschen sperren – dann geht nur Papierkorb |
| `--port` / `--host` | Standard `127.0.0.1:8765` |
| `--no-browser` | Browser nicht automatisch öffnen |
| `--no-cache` | Nichts auf Platte zwischenspeichern (siehe [Grenzen](#grenzen)) |
| `--refresh` | Sekunden zwischen automatischen Abgleichen, `0` schaltet ab (Standard 60) |
| `--version` | Version ausgeben und beenden |
| `--mcp` | Als MCP-Server über stdin/stdout laufen (siehe [unten](#für-ein-modell-erreichbar---mcp)) |
| `--mcp-readonly` | Dasselbe, aber nur lesend: kein Verschieben, Verschlagworten, Entwerfen |

## Wer darf ran

s3mail bindet auf `127.0.0.1` und kennt keine Benutzer. Was den Zugang schützt,
sind drei Prüfungen bei **jeder** Anfrage – jede gegen einen anderen Angriff:

- **Das Token** gegen Mitleser auf demselben Rechner. Es steht in der Adresse
  aus dem Terminal, und ohne es antwortet der Server nicht. Beim Ausliefern
  einer Seite wird es als Cookie mit `SameSite=Strict` gesetzt – deshalb
  funktionieren die Download-Links für Anhänge und `.eml`, ohne das Token in
  jeder URL mitzuschleppen.
- **Der `Host`-Header** gegen DNS-Rebinding. Eine fremde Domain, die auf
  `127.0.0.1` zeigt, wäre für den Browser dieselbe Herkunft wie s3mail und
  dürfte das Postfach auslesen – sie schickt aber ihren eigenen Namen im
  `Host`-Header mit, und der passt nicht.
- **Der `Origin`-Header** gegen CSRF. Eine fremde Seite kann per `fetch()` einen
  POST hierher schicken (Content-Type `text/plain`, kein Preflight). Lesen kann
  sie die Antwort nicht, aber Löschen und Versenden liefen trotzdem. Genau bei
  solchen Anfragen setzt der Browser die `Origin`.

Was das **nicht** ist: eine Anmeldung. Wer die Adresse mit dem Token hat, sieht
das ganze Postfach. Das genügt für ein Programm, das neben dem Browser auf dem
eigenen Rechner läuft – und trägt nicht weiter. `--host` auf eine öffentliche
Adresse zu legen heißt, das Postfach ins Netz zu stellen; dann gehört ein
Reverse-Proxy mit richtiger Auth davor.

Und dann **schaltet sich die Host-Prüfung selbst ab**: sie ergibt nur Sinn,
solange s3mail auf Loopback hört. Wer nach außen bindet, hat damit zwei der drei
Prüfungen aufgegeben – es bleibt das Token.

Zwei Dinge sind bewusst getrennt: **die Zugangsdaten zu AWS** liegen als
benanntes Profil in `~/.aws/credentials` (chmod 600), nie in s3mails eigener
Konfiguration. Und **die Adressdatei** `adresse.txt` im Konfigurationsverzeichnis
trägt dasselbe Token wie das Terminal – sie wird beim Beenden gelöscht, damit
keine abgelaufene Adresse liegen bleibt.

## Ordner sind echte S3-Prefixe

`--prefix mail/` ist die **Wurzel**. Was direkt darunter liegt, ist der Posteingang;
Unterordner sind Mailordner:

```
mail/                     ← Prefix aus der SES-Receipt-Rule   = Posteingang
mail/archiv/              ← Archiv
mail/spam/                ← Spam
mail/trash/               ← Papierkorb
mail/sent/                ← Kopie jeder verschickten Mail
mail/drafts/              ← Entwürfe, samt Blindkopie und Anhängen
mail/kunden/              ← selbst angelegt
mail/.s3mail-state.json   ← Tags, gelesen/ungelesen, Stern, Regeln (Snapshot)
mail/.s3mail-state/       ← die einzelnen Änderungen seit dem Snapshot
```

Verschieben = `CopyObject` + `DeleteObject`. Du siehst die Ordner also in der
S3-Konsole, und der Papierkorb lässt sich per Lifecycle-Regel automatisch leeren
(macht der Assistent auf Wunsch selbst):

```json
{ "Rules": [{ "ID": "s3mail-trash", "Status": "Enabled",
              "Filter": {"Prefix": "mail/trash/"},
              "Expiration": {"Days": 30} }] }
```

## Funktionen

**Lesen**

- Beim Indexieren wird pro Mail nur die erste 64 KB per Range-GET geholt – reicht für
  Header und Vorschautext. Index in `~/.cache/s3mail/`, Abgleich über ETags, 8 parallele
  Requests. „Neu laden“ holt nur Neues.
- HTML-Mails rendern in einem `sandbox=""`-iframe mit CSP; externe Bilder sind
  standardmäßig blockiert (Tracking-Pixel) und per Klick nachladbar.
- Anhänge einzeln herunterladbar, Rohmail als `.eml`.
- **Anhänge ansehen statt nur laden**: Bilder (PNG, JPEG, GIF, WebP, BMP) und PDF
  öffnen sich in einer Vorschau. Alles andere gibt es nur zum Herunterladen —
  **SVG ausdrücklich eingeschlossen**: es sieht aus wie ein Bild, ist aber XML
  mit Skriptunterstützung. Die Vorschau läuft in einem `sandbox=""`-iframe, und
  der Server antwortet mit einer CSP, die die Datei in einen eigenen Ursprung
  sperrt; sie kommt damit weder an das Token noch an die API.
- SES-Verdicts (`X-SES-Spam-Verdict`, `X-SES-Virus-Verdict`) als Badge.
- **SPF, DKIM und DMARC** aus `Authentication-Results`: in der Liste erscheint
  eine Marke, wenn eine der drei Prüfungen **fehlgeschlagen** ist – die
  Absenderadresse könnte gefälscht sein. In der geöffneten Mail steht, welche
  geprüft wurde. Kein Haken auf jeder Mail: „bestanden" ist der Normalfall, und
  eine Marke, die immer da ist, sieht bald niemand mehr an.
  Geglaubt wird dabei **nur der oberste Kopf, der `amazonses.com` nennt**.
  `Authentication-Results` ist gewöhnlicher Text in einer gewöhnlichen Mail:
  jeder Absender kann sich einen mit `dkim=pass` hineinschreiben. Der
  empfangende Server setzt seinen darüber, und nur der zählt.

**Schreiben**

- Antworten, Weiterleiten und neue Nachricht. Antworten hängen per `In-Reply-To`
  und `References` am Faden des Originals.
- **Signatur** aus den Einstellungen, unter jeder Mail. Sie steht **über** dem
  Zitat – am Ende eines langen Fadens sieht sie sonst niemand – und landet im
  Textfeld statt beim Versand, damit sie vorher noch kürzbar ist.
- **Adressen werden vorgeschlagen**, während man tippt — aus dem Index, nicht aus
  einem Adressbuch: ein zweiter Ort für Adressen ist ein zweiter Ort, an dem sie
  falsch stehen. Sortiert nach Häufigkeit, bei Gleichstand nach Aktualität.
  Gesucht wird in Adresse *und* Name; nachgeschlagen wird nur, was nach dem
  letzten Komma steht.
- **Anhänge**: Dateien anhängen, Cc und Blindkopie. Die Blindkopie geht über die
  Empfängerliste an SES und steht nie im Kopf der Mail – sonst hätten die
  Empfänger sie vor Augen. Bei mehr als 10 MB lehnt s3mail ab, bevor hochgeladen
  wird; das ist die Grenze, die SES zieht.
- **Gesendet**: jede verschickte Mail wird als Kopie in `<prefix>sent/` abgelegt.
  Schlägt das fehl, gilt die Mail trotzdem als verschickt – sie ist ja raus –,
  und es gibt einen Hinweis.
- **Kein Doppelversand nach einem Absturz.** Bevor SES gefragt wird, legt s3mail
  einen Vermerk neben den Entwurf (`<prefix>drafts/<name>.sending`) und streicht
  ihn erst durch, wenn Kopie und Entwurf erledigt sind. Stirbt das Programm
  dazwischen, holt der nächste Start nach, was feststeht; bleibt offen, ob SES
  die Mail genommen hat, fragt ein Balken über dem Postfach danach – statt sie
  stillschweigend ein zweites Mal zu verschicken. Der Vermerk ist ein Netz, keine
  Bedingung: lässt er sich nicht schreiben, geht die Mail trotzdem raus, mit
  Hinweis.
- **Entwürfe** liegen als echte Mail in `<prefix>drafts/`, samt Blindkopie und
  Anhängen. Sie überleben damit das geschlossene Fenster und sind auch vom
  zweiten Rechner aus zu sehen. Wer den Dialog mit getipptem Text schließt,
  bekommt den Entwurf gesichert statt weggeworfen. Beim Senden verschwindet er.

**Mehrere Postfächer**

Ein s3mail bedient beliebig viele Postfächer – `info@` und `support@` im selben
Bucket, oder Postfächer in verschiedenen AWS-Konten. Umgeschaltet wird oben links;
jedes Postfach hat eigenen Absender, eigene Signatur und eigenen Zustand.

Die Konfiguration führt sie als Liste:

```json
{"accounts": [
   {"bucket": "post", "prefix": "mail/info/",    "from": "info@firma.de"},
   {"bucket": "post", "prefix": "mail/support/", "from": "support@firma.de",
    "label": "Support"}],
 "port": 8765, "language": "de"}
```

Eine ältere Konfiguration mit einem einzelnen Postfach wird weiter gelesen und
beim nächsten Speichern umgeschrieben – es geht nichts verloren.

Scheitert ein Postfach beim Start (falsches Profil, kein Zugriff), kommen die
übrigen trotzdem hoch, und die Meldung nennt das fehlende.

**Sortieren**

- Ordner anlegen und verschieben, Papierkorb, Spam, Archiv, Gesendet, Entwürfe.
  Die Automatik fasst Papierkorb, Spam, Gesendet und Entwürfe nicht an – die
  ersten beiden sind eine Entscheidung, die letzten beiden sind selbst
  geschrieben.
- Endgültiges Löschen nur aus dem Papierkorb heraus und nur nach Bestätigung
  (serverseitig erzwungen, nicht nur in der UI).
- Tags mit Farben, mehrere pro Mail, Klick in der Seitenleiste filtert.
- Gelesen/ungelesen (Zähler pro Ordner), Stern.
- Mehrfachauswahl per Checkbox, Shift-Klick wählt einen Bereich; dann verschieben,
  taggen, markieren, löschen in einem Rutsch. Das Kästchen in der Werkzeugleiste
  wählt **alle Treffer** der aktuellen Suche — „alles von news@shop.io ins
  Archiv" ist damit zwei Klicks.
- Bleibt bei so einer Aktion eine Mail hängen, ziehen die anderen trotzdem um,
  und die Meldung nennt die Zahl, die wirklich angekommen ist. Scheitern zehn
  hintereinander, hört s3mail auf: dann stimmt etwas Grundsätzliches nicht, und
  eine Liste mit fünftausend gleichen Sätzen hilft niemandem.

**Automatische Regeln**

„Enthält X im Feld Von/An/Betreff/irgendwo → Ordner + Tags“. Laufen beim Indexieren auf
neue Mails; erste passende Regel gewinnt. Eine Mail wird nur einmal automatisch
einsortiert – wenn du sie zurückschiebst, bleibt sie liegen. Über „Auf alle bestehenden
Mails anwenden“ laufen die Regeln nachträglich übers ganze Postfach. Papierkorb und
Spam fasst die Automatik nicht an.

**Für die Arbeit am Kunden**

- **Wartet auf Antwort** – Filter in der Seitenleiste: was vor mindestens fünf
  Tagen rausging und worauf niemand geantwortet hat, längste Wartezeit zuerst.
  Als beantwortet gilt eine Mail, wenn von einem der Empfänger danach etwas kam –
  bewusst über die Adresse und nicht über den Faden: eine Antwort kommt oft als
  neue Mail, von einer Kollegin der angeschriebenen Person, oder mit einem Faden,
  den ein Ticketsystem abgeschnitten hat.
- **Verlauf** – Knopf in der Mailansicht: alle Mails mit dieser Adresse über alle
  Ordner, neueste zuerst. Das, was man vor einem Anruf vor sich haben will.
- **Textbausteine** – je Postfach in der Konfiguration, Auswahl im Verfassen-Dialog.
  Eingesetzt wird an der Cursorposition, nicht am Ende: ein Absatz gehört so oft in
  die Mitte einer Mail wie ans Ende.

```json
{"accounts": [{"bucket": "post", "prefix": "mail/", "from": "info@firma.de",
  "snippets": [{"name": "Terminvorschlag", "text": "passt Ihnen Dienstag 10 Uhr?"}]}]}
```

**Regelvorschläge**

Im Regeldialog steht, was du ohnehin schon von Hand tust: „Du hast 11 von 12
Mails von *news@shop.io* nach *Werbung* verschoben." Ein Klick legt die Regel als
Zeile ins Formular – gespeichert wird erst, wenn du speicherst.

Der Vorschlag stützt sich auf **Belege, nicht auf Geschmack**: mindestens vier
Mails desselben Absenders und mindestens 80 % davon im selben Ordner. Die Zahlen
stehen dabei, damit du sie prüfen kannst. Ausgewertet werden Absender, Ordner und
Tags – **nie der Inhalt einer Mail**, und ohne Netzverbindung oder API-Schlüssel.

Nicht vorgeschlagen wird: der Papierkorb (eine falsche Regel ließe Mail
verschwinden), der Posteingang (dort landet Mail von selbst, das ist keine
Entscheidung), und alles, was eine bestehende Regel schon fängt.

**Suche**

Freie Wörter suchen in Von/An/Betreff/Vorschau/Tags. Dazu Filter, kombinierbar:

```
rechnung from:kunde@x.de subject:"Angebot" after:2026-01-01 before:2026-08-01
has:anhang has:spam is:ungelesen is:stern tag:wichtig in:archiv
```

**Tastatur:** `n` neue Nachricht · `j`/`k` blättern · `x` auswählen · `s` Stern ·
`e` archivieren · `Entf` Papierkorb · `/` Suche.

**Sprache**

Die Oberfläche spricht Deutsch, Englisch und Spanisch. Die Auswahl steht oben rechts
im Postfach und im Assistenten; sie wird in der Konfiguration gemerkt
(`"language": "de"`) und gilt auch für die Meldungen beim Start und die Hilfe zu den
Optionen.

Ohne getroffene Wahl entscheidet der Browser (`Accept-Language`), ohne den Englisch.
Die Reihenfolge ist: Auswahl → Konfiguration → Browser → Englisch.

Die Suchbegriffe funktionieren in allen drei Sprachen: `from:` wie `von:` wie `de:`,
`is:ungelesen` wie `is:unread` wie `is:sinleer`. Ordnernamen sind dagegen echte
S3-Prefixe (`trash`, `spam`, `archiv`) und werden nur angezeigt, nicht übersetzt.

## Für ein Modell erreichbar: `--mcp`

s3mail spricht das Model Context Protocol über stdin/stdout – kein Fenster, kein
Webserver, kein Port. Damit kann Claude im Postfach suchen, lesen, einsortieren
und einen Entwurf schreiben.

**Voraussetzung:** s3mail muss einmal normal gelaufen und eingerichtet sein –
`--mcp` liest dieselbe `config.json` und startet keinen Assistenten. Ohne
Konfiguration bricht es mit einem Satz ab, der genau das sagt.

### Claude Code

```bash
claude mcp add s3mail -- /pfad/zu/s3mail --mcp
claude mcp list          # muss „✔ Connected" zeigen
```

Wieder abhängen: `claude mcp remove s3mail`.

### Claude Desktop

In `claude_desktop_config.json` (macOS:
`~/Library/Application Support/Claude/`, Windows: `%AppData%\Claude\`):

```json
{
  "mcpServers": {
    "s3mail": {
      "command": "/pfad/zu/s3mail",
      "args": ["--mcp"]
    }
  }
}
```

Danach Claude Desktop neu starten.

### Was Claude damit kann

| Werkzeug | wofür |
|---|---|
| `search` | Suchen wie im Postfach: `from:`, `subject:`, `after:`, `is:unread`, `tag:` … |
| `read` | Eine Mail ganz lesen, samt Namen der Anhänge |
| `folders` | Ordner mit Anzahl und Ungelesenen |
| `move` | Einsortieren – **Papierkorb und Spam werden abgelehnt**, sonst umkehrbar |
| `tag`, `flag` | Verschlagworten, gelesen/ungelesen, Stern |
| `draft` | Einen Entwurf in den Bucket legen |

Mit mehreren Postfächern nimmt jedes Werkzeug ein `account`-Argument; ohne
Angabe ist es das erste. Welche es gibt, steht in der Werkzeugbeschreibung, die
Claude ohnehin sieht.

Beispiele, die funktionieren: *„Was liegt seit einer Woche ungelesen im
Posteingang?"* · *„Sortier alles von news@shop.io ins Archiv."* · *„Schreib einen
Entwurf an den Kunden aus der letzten Rechnung, mit Terminvorschlag."*

Der MCP-Server darf gleichzeitig mit dem normalen s3mail laufen. Beide teilen
sich Index und Zustand – der Zustand liegt ohnehin als Op-Log im Bucket und
verträgt zwei Schreiber, der Index wird atomar ersetzt.

**Papierkorb und Spam sind keine Ziele für `move`.** Der Assistent bietet eine
Lifecycle-Regel an, die den Papierkorb nach 7, 30 oder 90 Tagen leert — ein
Verschieben dorthin ist damit ein Löschen mit Verzögerung. „Verschieb alles von
rechnung@ in den Papierkorb" ist ein Satz, der in eine Mail passt, und eine Mail
ist fremder Text. Der Versuch wird abgelehnt, mit Begründung; jedes andere
Verschieben bleibt erlaubt und umkehrbar.

Wer noch weniger zulassen will, startet mit `--mcp-readonly`: dann gibt es nur
`search`, `read` und `folders`. Die verändernden Werkzeuge werden nicht
angeboten **und** abgelehnt, falls sie doch aufgerufen werden.

**Es gibt kein Werkzeug zum Senden, und das ist der Punkt.** Eine eingehende Mail
ist fremder Text, der im Kontext des Modells landet; „schick das an…" passt in
eine Mail. Das Modell legt einen Entwurf in den Bucket, du öffnest s3mail und
drückst Senden. Der Absender bleibt technisch ein Mensch, nicht nur
organisatorisch. Wer `send` versucht, bekommt diese Begründung zurück statt einer
Fehlermeldung.

Jede gelesene Mail kommt mit dem Hinweis davor, dass der Inhalt Daten sind und
keine Anweisungen. Das löst Prompt-Injection nicht – es begrenzt, was daraus
folgen kann.

Kein API-Schlüssel, keine Netzverbindung zu Dritten: s3mail ruft kein Modell auf,
sondern macht sich für eines erreichbar.

## Selbst bauen

Fertige Pakete für macOS (Apple Silicon und Intel), Linux und Windows liegen
unter [Releases](https://git.ole-hartwig.eu/development/s3mail/-/releases). Wer
selbst bauen will, braucht nur Go – s3mail kommt ohne C-Bibliotheken aus, also
baut ein Rechner für alle:

```bash
cd go
go build ./cmd/s3mail                                  # für dieses System
GOOS=windows GOARCH=amd64 go build ./cmd/s3mail        # für ein anderes
```

Heraus kommt eine einzelne Datei von rund 15 MB, Start in Millisekunden. Die
Oberfläche steckt per `go:embed` mit drin, es gibt keinen Build-Schritt fürs
Frontend und nichts nachzuinstallieren.

Ausgeliefert wird als ZIP, und das mit Absicht: eine roh heruntergeladene Datei
verliert ihr Ausführungs-Bit und lässt sich dann gar nicht erst starten.

Für macOS steckt im ZIP ein `.app`-Bundle, kein nacktes Programm. Es wird in der
Pipeline mit einer Developer ID signiert und bei Apple notarisiert; das
Notarisierungsticket ist ans Bundle geheftet, damit auch ein Rechner ohne Netz
es prüfen kann. Für den Empfänger heißt das: doppelklicken, fertig – kein
`xattr -dr com.apple.quarantine`, kein Rechtsklick → Öffnen, keine Warnung.

```bash
unzip s3mail-macos-arm64.zip
open s3mail.app
```

Nachsehen lässt sich das an jedem heruntergeladenen Paket:

```bash
codesign -dv --verbose=4 s3mail.app     # Authority: Developer ID Application
xcrun stapler validate s3mail.app       # das geheftete Ticket
spctl -a -vvv -t install s3mail.app     # accepted, source=Notarized Developer ID
```

Windows bleibt unsigniert – dort meldet sich SmartScreen beim ersten Start mit
*Weitere Informationen → Trotzdem ausführen*.

### Wenn der Zugang wegfällt

Läuft die SSO-Sitzung ab, wird ein Schlüssel zurückgezogen oder eine Policy
enger, dann geht im Postfach nichts mehr — und keine Wiederholung im Browser
ändert daran etwas. s3mail erkennt diese Fälle und zeigt einen **Balken über
dem Postfach**, der stehen bleibt, mit dem Satz, der sagt, was zu tun ist
(`aws sso login --profile NAME`) und einem Knopf zum Erneut-Versuchen. Kein
Hinweis, der nach drei Sekunden verschwindet: das Problem ist nicht ein Klick,
sondern das ganze Postfach.

## Verschlüsselte Buckets

Es gibt zwei Sorten Verschlüsselung, und nur eine davon macht Arbeit.

**Serverseitig (SSE-S3 / SSE-KMS)** – die Standardverschlüsselung des Buckets. S3
entschlüsselt beim `GetObject` selbst, s3mail merkt davon nichts. Bei SSE-KMS braucht
die IAM-Rolle zusätzlich `kms:Decrypt` auf dem Schlüssel (und `kms:GenerateDataKey` zum
Schreiben, also für Tags, Verschieben und Papierkorb). Beim Verschieben liest s3mail die
Verschlüsselungseinstellung des Originals per `HeadObject` und gibt sie an `CopyObject`
weiter – die Kopie landet also nicht versehentlich unter dem Standardschlüssel des
Buckets. Speicherklasse (z. B. `STANDARD_IA`) wird genauso mitgenommen.

**Client-seitig (die KMS-Option in der SES-Receipt-Rule)** – hier verschlüsselt SES die
Mail, *bevor* sie in S3 landet. Im Bucket liegt dann kein MIME, sondern ein Umschlag:
der Datenschlüssel steckt von KMS verpackt in den Objekt-Metadaten
(`x-amz-key-v2`, `x-amz-iv`, `x-amz-cek-alg`, `x-amz-matdesc`), der Inhalt ist mit
AES-256 verschlüsselt. Ein normales `GetObject` liefert Kauderwelsch.

s3mail erkennt das an den Metadaten und macht es auf: `kms:Decrypt` auf dem verpackten
Schlüssel – mit dem Encryption Context aus `x-amz-matdesc`, sonst lehnt KMS ab – und
dann AES-GCM (aktuelles Format) bzw. AES-CBC (älteres). Das steckt im Programm
drin, nachzuinstallieren ist nichts. Zwei Folgen im Betrieb:

- Der Index kann keine Teilstücke mehr per Range-GET holen (ein halbes Chiffrat lässt
  sich nicht entschlüsseln). Sobald s3mail die erste verschlüsselte Mail sieht, lädt es
  Objekte komplett. Das erste Indexieren dauert dadurch länger und kostet mehr Traffic.
- Beim Verschieben bleibt der Umschlag unangetastet (`CopyObject` kopiert die Metadaten
  mit), es wird nichts neu verschlüsselt.

Ein gemischtes Postfach – ein paar verschlüsselte, ein paar unverschlüsselte Objekte –
ist kein Problem, das wird pro Objekt entschieden. Fehlt `kms:Decrypt`, fällt nur die
betroffene Mail als „nicht lesbar" aus, der Rest des Postfachs bleibt nutzbar.

Der Verbindungstest im Assistenten sagt dir, was Sache ist: er schaut sich eine echte
Mail an und meldet „client-seitig mit KMS – Entschlüsseln klappt", „serverseitig mit
KMS", „serverseitig (AES256)" oder „keine – die Mails liegen im Klartext".

## Zustand: geteilt, ohne Sperre

Tags, gelesen/ungelesen, Stern und Regeln stehen **im Bucket**, damit mehrere
Rechner denselben Stand sehen. Schlüssel ist der Objekt-Basename, nicht der volle
Key – deshalb überlebt der Zustand das Verschieben zwischen Ordnern.

Geschrieben wird aber nicht das ganze Dokument, sondern **die einzelne Änderung**.
Jeder Schreibvorgang legt ein kleines Objekt unter `<prefix>.s3mail-state/` ab,
auf dessen Schlüssel nur er selbst schreibt:

```
mail/.s3mail-state.json                          ← Snapshot, selten geschrieben
mail/.s3mail-state/20260820T2131...-0001-a7f3.json   {"ops":[{"t":"flags",…}]}
mail/.s3mail-state/20260820T2131...-0002-b1c9.json   {"ops":[{"t":"tags",…}]}
```

Zwei Rechner können sich dabei nicht ins Gehege kommen – es gibt keinen
gemeinsamen Schlüssel, auf den beide zeigen, und damit weder Sperre noch
`If-Match` noch `412`. Eine Mail als gelesen zu markieren kostet ein paar hundert
Byte statt des ganzen Postfachs.

Gelesen wird der Snapshot plus alle Änderungen, die neuer sind als sein
Wasserstand (`upto`), in Schlüsselreihenfolge. Die Schlüssel beginnen mit dem
Zeitstempel, sortieren sich also von selbst. Ab 50 offenen Änderungen wird
zusammengefasst: neuer Snapshot mit neuem Wasserstand, danach fliegen die
eingearbeiteten Objekte weg.

Der Wasserstand ist das, was die Sache gutmütig macht. Bleibt beim Aufräumen ein
Objekt liegen, weil das Löschen scheitert, wird es beim nächsten Laden
übersprungen statt ein zweites Mal angewandt – Löschen ist Müllabfuhr, keine
Buchhaltung.

Zwei Dinge, die man wissen sollte:

- **Die Uhr entscheidet die Reihenfolge.** Bei Änderungen, die aufeinander
  aufbauen – ein Tag umbenennen, den ein anderer Rechner gerade erst angelegt hat
  –, kann eine schief gehende Rechneruhr die Reihenfolge verdrehen. Für Tags
  setzen, lesen markieren und Sternchen ist die Reihenfolge egal, da gewinnt
  ohnehin die Vereinigung.
- **Alle Rechner sollten dieselbe Version fahren.** Eine ältere s3mail-Version
  liest nur den Snapshot und übersieht die Änderungen daneben.

Fehlt das Schreibrecht ganz, fällt s3mail still auf einen lokalen Zustand zurück
und zeigt das in der Seitenleiste an.

## IAM-Policy

```json
{
  "Version": "2012-10-17",
  "Statement": [
    { "Effect": "Allow", "Action": "s3:ListBucket",
      "Resource": "arn:aws:s3:::MEIN-BUCKET",
      "Condition": {"StringLike": {"s3:prefix": ["mail/*"]}} },
    { "Effect": "Allow", "Action": ["s3:GetObject", "s3:PutObject", "s3:DeleteObject"],
      "Resource": "arn:aws:s3:::MEIN-BUCKET/mail/*" },
    { "Effect": "Allow", "Action": "ses:SendRawEmail", "Resource": "*" },
    { "Effect": "Allow", "Action": ["sqs:ReceiveMessage", "sqs:DeleteMessage"],
      "Resource": "arn:aws:sqs:REGION:KONTO:MEINE-QUEUE" },
    { "Effect": "Allow", "Action": ["kms:Decrypt", "kms:GenerateDataKey"],
      "Resource": "arn:aws:kms:REGION:KONTO:key/DEIN-SCHLUESSEL" }
  ]
}
```

Der KMS-Teil entfällt, wenn weder der Bucket noch die SES-Regel verschlüsselt.

Der SQS-Teil entfällt, wenn es keine Klingel gibt – dann läuft der Abgleich im
Takt. **`sqs:GetQueueUrl` steht bewusst nicht dabei:** s3mail baut die URL aus
dem ARN, den es ohnehin in der Policy liest. Ein Aufruf und ein Recht weniger.

Damit dort etwas ankommt, braucht es die Gegenseite: die SES-Empfangsregel
benachrichtigt ein SNS-Topic, das Topic schreibt in die Queue. **Nicht** über
eine S3-Event-Notification auf dem Bucket – s3mail schreibt selbst ständig
hinein (Gelesen-Haken, Gesendet-Kopien, Entwürfe), und jede davon löste einen
Abgleich aus, der wieder schreibt. Über die Empfangsregel läuft nur eingehende
Mail. Ein Topic je Postfach, denn die SES-Benachrichtigung trägt Absender,
Empfänger und Betreff.

### Selbsteinrichtung: der Zugang beantwortet die Fragen des Assistenten

Bucket, Ordner, Absenderadresse und Queue stehen bereits in der Policy oben – als
genau die Angabe, an der AWS den Zugriff später misst. Darf der Zugang seine
eigene Policy lesen, holt s3mail sie sich von dort, statt danach zu fragen:
eintragen muss man dann nur noch Access Key und Secret.

```json
{ "Effect": "Allow", "Action": ["iam:ListUserPolicies", "iam:GetUserPolicy"],
  "Resource": "arn:aws:iam::KONTO:user/${aws:username}" }
```

`${aws:username}` hält das eng: sichtbar wird nichts als die Regeln, die für
den Aufrufer ohnehin gelten – kein fremdes Postfach, keine Kontoübersicht. Und
ein Weg um die eigene Beschränkung ist es nicht: eine Regel zu lesen ist nicht,
ein Objekt zu lesen.

Gelesen werden nur **eingebettete** Policies (`inline`). Angehängte
(`managed`) lassen sich nicht auf „meine eigene" einschränken – dafür Rechte zu
vergeben hieße, Einsicht in die Policies anderer Identitäten zu geben. s3mail
versucht sie, kommt ohne sie aus und sagt dazu nichts.

Die Region muss auch niemand heraussuchen: S3 nennt sie im Kopf seiner Antwort
(`x-amz-bucket-region`), und zwar selbst dann, wenn es die Anfrage ablehnt.

Fehlt das Recht, fragt der Assistent wie zuvor. Ein Fehler ist das nicht und es
wird auch keiner angezeigt.

### Optionale Rechte

Machen den Assistenten bequemer, mehr nicht – fehlt eins, werden die Felder
eingetippt statt ausgewählt:

| Recht | wofür |
|---|---|
| `s3:ListAllMyBuckets` | Bucket-Auswahlliste |
| `ses:ListIdentities`, `ses:GetIdentityVerificationAttributes` | Absender-Auswahlliste |
| `s3:GetLifecycleConfiguration`, `s3:PutLifecycleConfiguration` | Papierkorb-Automatik |

`s3:ListAllMyBuckets` ist mit Bedacht **nicht** in der Policy oben: es zeigt
jeden Bucket des Kontos, auch die, die mit Mail nichts zu tun haben. Bei einem
Postfach-Zugang ist die leere Auswahlliste deshalb der Normalfall – der Name
wird eingetippt oder kommt aus der Selbsteinrichtung.

## Nicht mehr schreiben (SES-Sperrliste)

Bittet jemand darum, nicht mehr angeschrieben zu werden, erledigt das der Knopf
**Nicht mehr schreiben** in der Mailansicht: die Adresse kommt auf die
Unterdrückungsliste des SES-Kontos, und SES nimmt keine Mail mehr an sie an.
Oben in der Kopfleiste zeigt **Gesperrt** die Liste und nimmt Adressen wieder
herunter.

Zwei Dinge dazu, die man wissen sollte:

- **Die Liste gehört dem AWS-Konto, nicht dem Postfach.** SES kennt keine
  Sperrliste pro Identität. Eine Adresse dort sperrt sie für alle Postfächer
  desselben Kontos. Bei „nicht mehr schreiben" ist das genau richtig – bei einem
  Tippfehler wäre es ärgerlich, deshalb gibt es den Weg zurück gleich daneben.
- **SES trägt auch selbst ein**, nämlich alles, was hart zurückkommt oder als
  Beschwerde gemeldet wird. In der Liste steht deshalb bei jedem Eintrag, woher
  er kommt.

Nötig sind dafür `ses:PutSuppressedDestination`,
`ses:DeleteSuppressedDestination`, `ses:ListSuppressedDestinations` und
`ses:GetSuppressedDestination`. Fehlen sie, fehlen auch die beiden Knöpfe nicht –
sie melden dann einen Rechtefehler im Klartext.

## Grenzen

- Kein IMAP: ein normales Mailprogramm kann das Postfach nicht öffnen. Senden
  ginge dort über den SMTP-Endpunkt von SES, lesen nicht – SES kennt keinen
  Postfachdienst.
- Push braucht eine Klingel in AWS: SES benachrichtigt ein SNS-Topic, das in eine
  SQS-Queue schreibt, an der s3mail hängt. Ist das eingerichtet, taucht neue Mail
  sofort auf; ohne läuft der Takt von `--refresh` (Standard 60 Sekunden) weiter.
  Die Queue liest s3mail aus der eigenen IAM-Policy – einzutragen ist nichts.
- Der Zugang hängt an einem Token, nicht an Benutzern (siehe
  [Wer darf ran](#wer-darf-ran)). Wer die Adresse aus dem Terminal hat, sieht das
  ganze Postfach. `--host` auf eine öffentliche Adresse zu legen heißt weiterhin,
  das Postfach ins Netz zu stellen – wenn, dann hinter einen Reverse-Proxy mit
  richtiger Auth.
- Verschieben kopiert das Objekt. Verschlüsselung und Speicherklasse werden dabei
  übernommen, die Versionshistorie eines versionierten Buckets aber nicht – der neue
  Key beginnt mit einer neuen Version.
- Bei sehr großen Postfächern (> ~50k Objekte) dauert das erste Indexieren; dann mit
  engerem Prefix arbeiten.
- **Was auf der Platte liegt, liegt dort im Klartext.** In `~/.cache/s3mail/`
  stehen der Index (Absender, Betreff, Vorschautext) **und die abgerufenen
  Mailtexte**, nach ETag geschlüsselt. Die Dateien haben `0600` in einem
  `0700`-Verzeichnis, ein anderer Benutzer desselben Rechners kommt also nicht
  heran — wohl aber ein Backup (Time Machine), ein Ordnersync (Dropbox, iCloud)
  und jeder, der die Platte ohne FileVault in die Hand bekommt.
  **Das trifft auch client-seitig verschlüsselte Mail:** s3mail muss sie zum
  Anzeigen entschlüsseln, und danach liegt sie dort entschlüsselt. Wer das nicht
  will, startet mit `--no-cache` — dann bleibt nichts zurück, dafür wird bei
  jedem Start neu indexiert und jede Mail bei jedem Öffnen neu geholt. Ein
  verschlüsselter Cache mit einem Schlüssel aus dem Schlüsselbund des Systems
  wäre der bessere Weg; er kostet plattformabhängigen Code (macOS Keychain,
  Windows DPAPI, Linux Secret Service) und ist deshalb nicht gebaut.
- Die **Windows**-Pakete sind nicht signiert, dort meldet sich SmartScreen. Die
  macOS-Pakete sind signiert und notarisiert, für Linux stellt sich die Frage nicht.

## Sicherheit

Meldeweg, Annahmen und das Bedrohungsmodell stehen in
[`SECURITY.md`](SECURITY.md) — samt dem, was **nicht** abgedeckt ist. Kurz:
Lücken an `security@ole-hartwig.eu`, nicht als Issue.

## Lizenz

Apache-2.0. Der volle Text steht in [`LICENSE`](LICENSE), die Namensnennung in
[`NOTICE`](NOTICE); jede Quelldatei trägt den SPDX-Bezeichner im Kopf.

Kurz, ohne Anspruch auf Rechtsberatung: benutzen, ändern, weitergeben und
verkaufen ist erlaubt, auch geschlossen. Beizulegen sind Lizenz und NOTICE, und
geänderte Dateien müssen als geändert gekennzeichnet sein. Die Patentklausel in
Abschnitt 3 ist der Grund für Apache-2.0 statt MIT: sie gibt jedem Nutzer
ausdrücklich die Patentrechte an dem, was hier drinsteckt, und nimmt sie dem
wieder weg, der deswegen klagt.

## Tests

```bash
cd go && go test ./...
```

Zehn Pakete, gut hundert Testfunktionen, kein AWS-Zugriff: `s3fake` bildet S3 mit
ETags, Präfix-Listing, Objekt-Metadaten und serverseitiger Verschlüsselung nach.
Abgedeckt sind Index, Ordner, Verschieben samt Zustandsübernahme,
Papierkorb-Regeln, Tags, Regel-Engine, Suche, Versand-Header, Zustand von
mehreren Rechnern (Ops, Zusammenfassen, Wasserstand), KMS-Entschlüsselung (GCM
und CBC), MIME-Zerlegung, Konfiguration, Verbindungstest, Lifecycle,
HTTP-Schicht, Zugangskontrolle und Fehlerübersetzung.

Eine einzelne Gruppe:

```bash
go test ./store/ -run TestVerschiebenNimmtDenZustandMit -v
```
