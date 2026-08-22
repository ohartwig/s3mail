# s3mail

Mail-Client für E-Mails, die Amazon SES als Rohdaten (MIME) in einen S3-Bucket schreibt.
Eine einzige Python-Datei, startet einen lokalen Webserver, Postfach im Browser.

## Voraussetzungen

### Auf dem Rechner, der s3mail startet

Zwei Wege, und beide brauchen sonst nichts – keinen Webserver, keine Datenbank,
kein Docker. s3mail bringt seinen eigenen Server mit und bindet ihn an 127.0.0.1.

- **Fertiges Paket** aus den
  [Releases](https://git.ole-hartwig.eu/development/s3mail/-/releases) – da ist
  alles drin, Python muss nicht installiert sein. Derzeit nur macOS auf Apple
  Silicon; für andere Plattformen siehe
  [Ohne Python: fertiges Paket](#ohne-python-fertiges-paket).

  ```bash
  unzip s3mail-macos-arm64.zip
  xattr -dr com.apple.quarantine s3mail   # einmalig, das Programm ist unsigniert
  ./s3mail/s3mail
  ```
- **Aus dem Quellcode:** Python 3.10 oder neuer und `pip install boto3`. Die
  Untergrenze kommt von boto3, s3mail selbst läuft auch auf älteren. Sind die
  Mails client-seitig mit KMS verschlüsselt, kommt `cryptography` dazu.

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

**Tastatur:** `n` neue Nachricht · `j`/`k` blättern · `x` auswählen · `s` Stern ·
`e` archivieren · `Entf` Papierkorb · `/` Suche.

## Ohne Python: fertiges Paket

Fertig gebaut liegt das jeweils aktuelle Paket unter
[Releases](https://git.ole-hartwig.eu/development/s3mail/-/releases) – bisher nur
für **macOS auf Apple Silicon**. Zwei Assets:

- `s3mail-macos-arm64.zip` – der entpackte Ordner, **empfohlen**, startet in einer
  halben Sekunde.
- `s3mail-macos-arm64-onefile.zip` – eine einzelne Datei, bequemer zum Weitergeben,
  aber rund 7 Sekunden pro Start.

Beide sind ZIPs, und zwar mit Absicht: eine roh heruntergeladene Datei verliert ihr
Ausführungs-Bit und lässt sich dann gar nicht erst starten. Dazu hängt macOS jedem
Download ein Quarantäne-Attribut an, und weil das Programm nicht signiert ist,
blockt Gatekeeper. Beides ist einmalig erledigt:

```bash
unzip s3mail-macos-arm64.zip
xattr -dr com.apple.quarantine s3mail
./s3mail/s3mail
```

Wer lieber klickt: Finder → Rechtsklick auf `s3mail` → Öffnen, dann bestätigen.

Für jede andere Plattform einmal selbst bauen – PyInstaller baut immer für das
System, auf dem es läuft:

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

Wer den Empfängern die Gatekeeper-Warnung ersparen will, braucht ein
Apple-Developer-Zertifikat und muss signieren und notarisieren
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

### Selbsteinrichtung: der Zugang beantwortet die Fragen des Assistenten

Bucket, Ordner und Absenderadresse stehen bereits in der Policy oben – als
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
und echt verschlüsselten Testmails – 38 Testgruppen: Index, Ordner, Verschieben mit
Zustandsübernahme, Papierkorb-Regeln, Tags, Regel-Engine, Suche, Versand-Header,
Zustand von mehreren Rechnern (Ops, Zusammenfassen, Wasserstand), KMS-Entschlüsselung
(GCM und CBC), Konfiguration, Credentials-Datei, Verbindungstest, Lifecycle,
HTTP-Schicht, Zugangskontrolle, Fehlerübersetzung. Kein AWS-Zugriff, aber `boto3`
und `cryptography` müssen installiert sein.

`ui_check.py` und `ui_setup_check.py` klicken zusätzlich mit Playwright durch die
laufende Oberfläche (Postfach bzw. Assistent).

```bash
python3 test_s3mail.py
```
