# Spike: MIME-Schicht in Go

Vorabprüfung für eine mögliche Portierung von s3mail nach Go. Beantwortet die eine
Frage, an der die Portierung scheitern würde, wenn sie scheitert: **liest Go
eingehende Mail so gut wie Pythons `email`-Paket?**

## Ergebnis

Ja, mit `github.com/emersion/go-message`. Von 15 bewusst gemeinen Testmails
stimmen 12 exakt mit dem Python-Parser überein. Die drei Abweichungen sind
allesamt Stellen, an denen **Go besser ist** – sie stehen als begründete Einträge
in `abweichungen` in `mime_test.go`:

| Fall | Python | Go |
|---|---|---|
| Roher 8-Bit-Header (windows-1252, nicht RFC-2047-kodiert) | `Angebot �ber 250�` – Text verloren | `Angebot über 250€` |
| Bild mit `Content-ID`, auf das das HTML per `cid:` zeigt | zählt als Anhang – jede Signatur mit Logo bekommt eine Büroklammer | weder Anhang noch Vorschautext |
| Body ist UTF-8, deklariert ist `us-ascii` | `Hier stehen ��mlaute` | `Hier stehen Ümlaute` |

Die ersten beiden sind damit auch Fehlerberichte gegen die aktuelle
Python-Fassung.

## Cross-Kompilierung

Der eigentliche Grund für Go. Alle vier Ziele von einem beliebigen Rechner aus,
ohne Runner pro Plattform:

```
windows/amd64    3,7M
darwin/arm64     3,6M
darwin/amd64     3,7M
linux/amd64      3,6M
```

(Nur die MIME-Schicht plus ein Testprogramm; mit AWS-SDK und Oberfläche wird es
mehr, bleibt aber je **eine Datei** ohne Entpacken beim Start.)

## Laufen lassen

```bash
go test ./...                                   # Vergleich gegen Python
go test -v ./...                                # zeigt die bewussten Abweichungen
go run ./cmd/probe testdata/corpus/05-nested-mixed.eml
```

## Wie der Vergleich entsteht

`testdata/corpus/*.eml` sind 15 Mails mit den Fällen, an denen Parser scheitern:
RFC-2047 in base64 und quoted-printable, über zwei Zeilen gefaltete Header,
windows-1252, verschachteltes multipart, RFC-2231-Dateinamen, rohe Umlaute im
Dateinamen, inline-Bilder, fehlender Content-Type, gelogener Zeichensatz, gar kein
MIME, krumme Datumsformate, Kommas in Anzeigenamen.

`testdata/expected.json` ist das, was der **bestehende Python-Parser** daraus
macht – erzeugt mit den echten Funktionen aus `s3mail_core.py`, nicht von Hand
geschrieben. Der Go-Test vergleicht Feld für Feld dagegen. Wächst der Korpus um
echte Mails aus dem Bucket, wird die Erwartungsdatei neu erzeugt.

---

# Portierung: `core`

Zustand (Op-Log), Ordnerlogik, Regel-Engine und Suche. Spricht weder mit S3 noch
mit HTTP – deshalb lässt sich die Schicht vollständig gegen die Python-Fassung
prüfen, ohne AWS und ohne Netz.

## Geprüft, nicht behauptet

Dieselbe Methode wie beim MIME-Spike: **Python erzeugt die Erwartung, Go muss sie
reproduzieren.** Alles unter `core/testdata/` ist aus `s3mail_core.py` exportiert,
nichts davon von Hand geschrieben.

| Datei | Woher | Was sie absichert |
|---|---|---|
| `index.json`, `state.json` | echter `MailStore` gegen den Fake-S3 | Ausgangslage für alle Vergleiche |
| `searches.json` | 24 Abfragen durch `MailStore.search` | Treffer **und** Reihenfolge |
| `ops.json`, `ops_result.json` | 14 Operationen durch `apply_op` | Ergebnis der Op-Folge |
| `folders.json` | 25 Ordnernamen durch `valid_folder` | was ein gültiger Name ist |
| `rulehits.json` | 7 Regeln × 5 Mails durch `_rule_hits` | Regeltreffer |

Dazu Tests, die keine Entsprechung in Python haben, weil sie Eigenschaften prüfen
statt Werte:

- **`TestOpsIdempotent`** ist die Invariante, auf der der Wasserstand ruht: jede
  Operation zweimal angewandt muss dasselbe ergeben wie einmal. Fällt der Test,
  ist das Op-Log-Design kaputt, nicht der Test. `TestOpsEinzelnIdempotent` zeigt
  zusätzlich, *welche* Operation es wäre.
- **`TestZweiRechner`** fährt zwei Änderungsfolgen in beiden Reihenfolgen und
  prüft, dass in keiner etwas verlorengeht.
- **`TestSicherheitsgrenze`** hält fest, dass weder Snapshot noch Ops-Ordner über
  einen Key erreichbar sind.

19 Testfunktionen, 91 % der Anweisungen in `core` abgedeckt.

## Absichtlich noch nicht hier

`PlanRules` entscheidet nur, *was* zu tun wäre, und führt nichts aus – das
Verschieben in S3 macht die Schicht darüber. Damit bleibt die Regel-Engine ohne
Netz testbar.

---

# Portierung: `store`

Alles, was mit S3 spricht: Index, Verschieben, Löschen, die Persistenz des
Zustands als Op-Log und die KMS-Entschlüsselung. Der Zugriff läuft über eine
schmale Schnittstelle (`S3`, `KMS`), deshalb braucht kein Test ein AWS-Konto.

## KMS gegen echte Chiffrate geprüft

`testdata/envelopes.json` enthält Umschläge, die die **Python-Testsuite erzeugt
hat** – AES-GCM (aktuelles Format) und AES-CBC (älteres), mit den echten
Metadaten. Der Go-Code entschlüsselt beide zum selben Klartext. Dazu geprüft:
der Encryption Context aus `x-amz-matdesc` wird durchgereicht (ohne ihn lehnt KMS
ab), fehlende Rechte kippen nur die betroffene Mail, und Metadaten-Schlüssel
werden unabhängig von der Schreibweise erkannt.

## Was die Tests festhalten

- **Ein Op-Objekt pro Änderung**, nicht das ganze Dokument – inklusive Prüfung,
  dass es unter 200 Byte bleibt und kein Snapshot geschrieben wird.
- **Zwei Rechner ohne Konflikt**: beide schreiben, keiner überschreibt, ein
  dritter sieht beide Änderungen.
- **Wasserstand**: ein Op-Objekt, dessen Löschen scheiterte und das wieder
  auftaucht, wird übersprungen statt erneut angewandt.
- **Schreibfehler** behält die Änderung lokal und holt sie beim nächsten Versuch
  nach; ohne Schreibrecht trägt die lokale Datei.
- **Verschieben** nimmt Verschlüsselung und Speicherklasse mit, zieht den Zustand
  nach und entschärft Namenskollisionen (`m1` → `m1-1`), ohne die bestehende Mail
  zu überschreiben.
- **Löschen** nur aus dem Papierkorb, mit `--no-delete` gar nicht – geprüft am
  Store, nicht an der Oberfläche.
- **Verschlüsselte Postfächer** fordern kein Teilstück mehr an, sobald das erste
  solche Objekt auftaucht; eine Mail ohne `kms:Decrypt` fällt einzeln aus, der
  Rest des Index bleibt brauchbar.

## Ein Fehler, den der Test gefunden hat

Zwei Rechner, die in derselben Mikrosekunde schrieben, konnten denselben Op-Namen
erzeugen – einer der beiden wäre überschrieben worden. Jeder `State` hat jetzt
eine eigene Kennung aus `crypto/rand`, die im Namen steckt:

```
20260821T100001.000000-a3f9c21b4e07-0001.json
└ Zeitstempel (sortiert)  └ Prozess       └ laufende Nummer
```

45 Testfunktionen über alle Pakete, 82–91 % Anweisungsabdeckung.
