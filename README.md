# s3mail

Mail-Client für E-Mails, die Amazon SES als Rohdaten (MIME) in einen S3-Bucket schreibt.
Eine einzige Python-Datei, startet einen lokalen Webserver, Postfach im Browser.

## Erster Start

```bash
pip install boto3
python3 s3mail.py
```

Wenn deine SES-Regel die Mails client-seitig mit KMS verschlüsselt, kommt noch
`pip install cryptography` dazu – siehe [Verschlüsselte Buckets](#verschlüsselte-buckets).

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
   Adressen).
3. **Prüfen** – legt kurz ein Testobjekt an und löscht es wieder. Ergebnis ist eine
   Checkliste: Bucket lesen, Mail lesen, Schreiben, Löschen, Zustand von mehreren
   Rechnern, SES-Absender. Was fehlt, steht im Klartext dabei, inklusive der IAM-Aktion.
   Danach lässt sich optional eine Lifecycle-Regel setzen, die den Papierkorb nach
   7/30/90 Tagen automatisch leert.

„Speichern und starten“ schreibt `~/.config/s3mail/config.json` (chmod 600) und lädt
direkt das Postfach. Ab dann genügt `python3 s3mail.py`. Über den Knopf
**Einstellungen** oben rechts kommt man jederzeit zurück in den Assistenten.

Wer lieber Argumente tippt, kann alles weiterhin per CLI setzen – die überschreiben die
Konfigurationsdatei für den jeweiligen Start:

```bash
python3 s3mail.py --bucket mein-mail-bucket --prefix mail/ \
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

## Ordner sind echte S3-Prefixe

`--prefix mail/` ist die **Wurzel**. Was direkt darunter liegt, ist der Posteingang;
Unterordner sind Mailordner:

```
mail/                     ← Prefix aus der SES-Receipt-Rule   = Posteingang
mail/archiv/              ← Archiv
mail/spam/                ← Spam
mail/trash/               ← Papierkorb
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
- SES-Verdicts (`X-SES-Spam-Verdict`, `X-SES-Virus-Verdict`) als Badge.

**Sortieren**

- Ordner anlegen und verschieben, Papierkorb, Spam, Archiv.
- Endgültiges Löschen nur aus dem Papierkorb heraus und nur nach Bestätigung
  (serverseitig erzwungen, nicht nur in der UI).
- Tags mit Farben, mehrere pro Mail, Klick in der Seitenleiste filtert.
- Gelesen/ungelesen (Zähler pro Ordner), Stern.
- Mehrfachauswahl per Checkbox, Shift-Klick wählt einen Bereich; dann verschieben,
  taggen, markieren, löschen in einem Rutsch.

**Automatische Regeln**

„Enthält X im Feld Von/An/Betreff/irgendwo → Ordner + Tags“. Laufen beim Indexieren auf
neue Mails; erste passende Regel gewinnt. Eine Mail wird nur einmal automatisch
einsortiert – wenn du sie zurückschiebst, bleibt sie liegen. Über „Auf alle bestehenden
Mails anwenden“ laufen die Regeln nachträglich übers ganze Postfach. Papierkorb und
Spam fasst die Automatik nicht an.

**Suche**

Freie Wörter suchen in Von/An/Betreff/Vorschau/Tags. Dazu Filter, kombinierbar:

```
rechnung from:kunde@x.de subject:"Angebot" after:2026-01-01 before:2026-08-01
has:anhang has:spam is:ungelesen is:stern tag:wichtig in:archiv
```

**Tastatur:** `j`/`k` blättern · `x` auswählen · `s` Stern · `e` archivieren ·
`Entf` Papierkorb · `/` Suche.

## Ohne Python: Binary bauen

Wer s3mail an jemanden weitergibt, der kein Python hat, baut ein Paket mit
PyInstaller. Gebraucht wird das nur auf dem Rechner, der baut:

```bash
pip install pyinstaller boto3 cryptography
python3 build.py                    # s3mail.py aktualisieren
pyinstaller --clean s3mail.spec     # -> dist/s3mail/
```

Heraus kommt `dist/s3mail/` – ein Ordner mit dem Startprogramm `s3mail` darin,
zum Weitergeben einfach zippen. Rund 37 MB, Start in einer halben Sekunde.

`S3MAIL_ONEFILE=1 pyinstaller --clean s3mail.spec` macht daraus stattdessen eine
einzelne 15-MB-Datei. Bequemer zum Verschicken, aber auf macOS spürbar zäh: die
Datei packt sich bei *jedem* Start neu aus, und XProtect sieht sich das
Ausgepackte jedes Mal an – gemessen rund 7 Sekunden pro Start gegenüber einer
halben Sekunde beim Ordner. Unter Linux und Windows fällt das weg.

Zwei Dinge, die man wissen muss:

- **Pro Plattform einmal bauen.** PyInstaller baut immer für das System, auf dem
  es läuft. Ein macOS-arm64-Paket läuft weder unter Windows noch auf einem
  Intel-Mac.
- **macOS zeigt eine Warnung**, weil das Programm nicht signiert ist – beim ersten
  Start über Rechtsklick → Öffnen bestätigen. Wer das den Empfängern ersparen
  will, braucht ein Apple-Developer-Zertifikat und muss signieren und notarisieren
  (`codesign_identity` in `s3mail.spec`).

Die Spec-Datei wirft die Dienstbeschreibungen aus botocore weg, die s3mail nie
anfasst – von rund 400 AWS-Diensten bleiben S3, SES, KMS und STS übrig, was etwa
20 MB spart.

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
dann AES-GCM (aktuelles Format) bzw. AES-CBC (älteres). Dazu braucht es das Paket
`cryptography`. Zwei Folgen im Betrieb:

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
    { "Effect": "Allow", "Action": ["kms:Decrypt", "kms:GenerateDataKey"],
      "Resource": "arn:aws:kms:REGION:KONTO:key/DEIN-SCHLUESSEL" }
  ]
}
```

Der KMS-Teil entfällt, wenn weder der Bucket noch die SES-Regel verschlüsselt.

Optional, macht den Assistenten bequemer: `s3:ListAllMyBuckets` (Bucket-Dropdown),
`ses:ListIdentities` + `ses:GetIdentityVerificationAttributes` (Absender-Dropdown),
`s3:GetLifecycleConfiguration` + `s3:PutLifecycleConfiguration` (Papierkorb-Automatik).
Fehlt eins davon, funktioniert der Assistent trotzdem – die Felder werden dann
eingetippt statt ausgewählt.

## SES-Seite (Kurzfassung)

1. Domain in SES verifizieren (DKIM-CNAMEs setzen).
2. MX-Record der Domain auf `inbound-smtp.<region>.amazonaws.com` (Priorität 10).
3. Receipt-Rule-Set anlegen, Regel mit Aktion **S3** → Bucket + Prefix `mail/`.
4. Bucket-Policy muss SES das Schreiben erlauben (`ses.amazonaws.com`, Bedingung
   `aws:SourceAccount` = deine Account-ID) – die SES-Konsole bietet das an.
5. Rule-Set aktivieren. Testmail schicken, dann in s3mail „Neu laden“.

## Grenzen

- Kein IMAP, kein Push – neue Mails kommen erst mit „Neu laden“.
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

## Tests

`test_s3mail.py` fährt die ganze Logik gegen einen Fake-S3 mit ETags, Präfix-Listing
und echt verschlüsselten Testmails – 36 Testgruppen: Index, Ordner, Verschieben mit
Zustandsübernahme, Papierkorb-Regeln, Tags, Regel-Engine, Suche, Versand-Header,
Zustand von mehreren Rechnern (Ops, Zusammenfassen, Wasserstand), KMS-Entschlüsselung
(GCM und CBC), Konfiguration, Credentials-Datei, Verbindungstest, Lifecycle,
HTTP-Schicht, Zugangskontrolle. Kein AWS-Zugriff, aber `boto3` und `cryptography`
müssen installiert sein.

`ui_check.py` und `ui_setup_check.py` klicken zusätzlich mit Playwright durch die
laufende Oberfläche (Postfach bzw. Assistent).

```bash
python3 test_s3mail.py
```
