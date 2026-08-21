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
