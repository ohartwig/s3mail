# Sicherheit

## Eine Lücke melden

**security@ole-hartwig.eu.** Bitte nicht als GitLab-Issue — ein offenes Ticket
ist die Veröffentlichung.

Was hilft: was du getan hast, was passiert ist, was du erwartet hättest. Eine
Beispielmail oder ein Bucket-Layout, an dem es sich nachstellen lässt, ist mehr
wert als eine Einschätzung des Schweregrads.

Ich antworte innerhalb von fünf Werktagen. Es gibt kein Bug-Bounty-Programm und
keine Belohnung; wer möchte, wird im Release-Text genannt.

Gepflegt wird die jeweils letzte Version. Für ältere gibt es keine Rückportierung
— es ist ein einzelnes Programm, das Aktualisieren kostet einen Download.

## Was s3mail ist

Ein Programm auf dem eigenen Rechner, das einen Webserver an `127.0.0.1` bindet
und ein Postfach im Browser zeigt. Kein Dienst, keine Benutzerverwaltung, keine
Mandanten. Die Mail liegt in einem S3-Bucket, verschickt wird über SES.

Daraus folgt der ganze Rest dieses Dokuments: **die Vertrauensgrenze ist der
Rechner**, nicht der Prozess.

## Annahmen

Ohne diese drei ist das Modell darunter gegenstandslos:

1. **Der Rechner ist nicht kompromittiert.** Wer dort Code ausführt, hat das
   Postfach — das kann kein Server auf `127.0.0.1` verhindern, und s3mail
   versucht es auch nicht.
2. **Der AWS-Zugang ist so eng wie der Assistent ihn vorschlägt.** Eine
   Bucket-Policy, die mehr erlaubt, liegt außerhalb von s3mail.
3. **S3 und SES tun, was AWS zusagt.** Dass ein Objekt mit `0600`-Rechten im
   Bucket liegt, prüft s3mail nicht nach.

## Angreifer und was dagegen steht

| Angreifer | Weg | Abwehr | Was bleibt |
|---|---|---|---|
| Absender einer Mail | HTML und JavaScript in der Mail | `sandbox=""`-iframe, CSP, externe Bilder blockiert | Nur so gut wie der Browser. Ein alter Browser ist das Risiko, nicht die Mail |
| Absender | Tracking-Pixel | standardmäßig blockiert, Nachladen ist ein Klick | Wer klickt, verrät sich selbst — bewusst, aber er verrät sich |
| Absender | Gefälschte Absenderadresse | SPF/DKIM/DMARC aus `Authentication-Results`; geglaubt wird nur der oberste Kopf, der `amazonses.com` nennt | Sagt der Kopf nichts, steht nichts da. Eine Mail von vor der Einrichtung ist ungeprüft, nicht schlecht |
| Absender | Prompt-Injection über den MCP-Server | Kein `send`. `move` lehnt Papierkorb und Spam ab. Vor jeder gelesenen Mail steht, dass ihr Inhalt Daten sind | `move` in gewöhnliche Ordner, `tag` und `draft` bleiben steuerbar: eine injizierte Mail kann Post einsortieren, wo sie nicht hingehört, und Entwürfe mit fremdem Text anlegen. Beides ist sichtbar und umkehrbar |
| Absender | Bösartiger Anhang | Keine. Herunterladen ist Herunterladen | Kein Virenscan. Das SES-Virus-Verdict ist ein Hinweis, keine Prüfung |
| Fremde Website im selben Browser | CSRF, DNS-Rebinding | `Origin`-Prüfung, `Host`-Prüfung bei jeder Anfrage | — |
| Anderer Prozess auf dem Rechner | Ruft `127.0.0.1` auf | Token in der Startadresse, danach `SameSite=Strict`-Cookie. `adresse.txt` hat `0600` und wird beim Beenden gelöscht; in der Prozessliste steht das Token nicht (es liegt nicht in `argv`) | Ein Prozess **desselben Benutzers** liest `adresse.txt` und kommt damit ins Postfach. Dagegen hilft nichts auf dieser Ebene — siehe Annahme 1 |
| Anderer Benutzer desselben Rechners | Konfiguration und Cache lesen | `0600` auf Konfiguration, Index, Mailtexte und Zustandsdatei, `0700` auf die Verzeichnisse | Nichts — solange die Dateirechte gelten |
| Wer an ein Backup kommt | Time Machine, Ordnersync, Platte ohne FileVault | Keine | **Der Cache liegt im Klartext**, samt der Mailtexte, und das schließt client-seitig verschlüsselte Mail ein: s3mail muss sie zum Anzeigen entschlüsseln. `--no-cache` schaltet das ab, ein verschlüsselter Cache ist nicht gebaut |
| Zweiter Rechner am selben Bucket | Zustand kaputtschreiben | Op-Log, ein Schlüssel je Vorgang, alle Ops idempotent | Kein Schutz gegen absichtlich falsche Ops. Das ist dieselbe Vertrauensstufe wie Schreibrecht auf den Bucket |
| Wer einen AWS-Schlüssel hat | Alles | Keine, und das ist richtig so | Außerhalb von s3mail. Empfehlung: SSO mit kurzen Sitzungen statt Langzeitschlüssel |
| Lieferkette | Gefälschtes Programm | macOS: Developer ID signiert und bei Apple notarisiert. Alle Plattformen: SHA256 je Paket | Windows und Linux sind nicht signiert. Ein signiertes Release-Manifest ist geplant, aber nicht gebaut |
| Netzwerk | Mitlesen auf dem Weg zu AWS | TLS über das AWS-SDK | — |

## Was ausdrücklich nicht abgedeckt ist

- **Kein Virenscan von Anhängen.** Ein Anhang wird heruntergeladen, nicht
  geprüft.
- **Kein Schutz bei kompromittiertem Rechner.** Siehe Annahme 1.
- **Kein Mehrbenutzerbetrieb.** s3mail kennt keine Benutzer und keine Rollen.
  Wer es ins Netz stellt, stellt ein Postfach ohne Anmeldung ins Netz — wenn,
  dann hinter einen Reverse-Proxy mit eigener Authentifizierung.
- **Keine Sperre gegen einen Menschen mit Zugriff.** Wer den Rechner benutzt,
  benutzt das Postfach.

## Der MCP-Server im Besonderen

Der Zuschnitt ist die Sicherheitsentscheidung, nicht eine Bequemlichkeit:

- **Kein `send`.** Eine eingehende Mail ist fremder Text im Kontext des Modells,
  und „schick das an…" passt in eine Mail. Das Modell legt einen Entwurf ab, ein
  Mensch drückt Senden. Die Freigabestelle ist nicht eine Regel, an die sich
  jemand erinnern muss, sondern der einzige Weg, der existiert.
- **Kein Verschieben in Papierkorb oder Spam.** Der Assistent bietet eine
  Lifecycle-Regel an, die den Papierkorb nach 7, 30 oder 90 Tagen leert; ein
  Verschieben dorthin wäre ein Löschen mit Verzögerung. Das war bis
  August 2026 offen und ist geschlossen.
- **`--mcp-readonly`** für alle, die gar keine Änderung wollen: dann gibt es nur
  `search`, `read` und `folders`.

Was bleibt: `move` in gewöhnliche Ordner, `tag` und `draft`. Eine injizierte Mail
kann damit Post umsortieren und Entwürfe mit fremdem Text anlegen. Beides ist im
Postfach sichtbar, und beides ist umkehrbar — das ist die Grenze, die hier
gezogen wurde, und sie steht hier, damit sie nicht überrascht.

## Was s3mail selbst nicht sendet

Keine Telemetrie, keine Absturzberichte, keine Aktualisierungsprüfung. Nach außen
gehen nur HTTPS-Verbindungen zu `s3.<region>.amazonaws.com`,
`email.<region>.amazonaws.com`, bei Verschlüsselung `kms.<region>.amazonaws.com`
und bei eingerichtetem Push `sqs.<region>.amazonaws.com`.
