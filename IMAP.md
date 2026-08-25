# IMAP für s3mail — Entwurf, Schritt 1 gebaut

Stand 2026-08-24. Aufgeschrieben, damit die Überlegung nicht zweimal gemacht
werden muss. **Gebaut ist bisher die UID-Vergabe** (`core/uid.go`, Schritt 1
unten); alles Weitere steht noch aus. Es gibt keinen IMAP-Server.

## Warum das der größte Hebel ist

Spricht s3mail IMAP, funktioniert Apple Mail auf iPhone, iPad und Mac — ohne
App, ohne App-Store-Prüfung, ohne Provision. Dasselbe gilt für Thunderbird,
Outlook und alles andere. Kein anderes Vorhaben erreicht so viele Geräte mit so
wenig neuem Produkt.

Der Vergleich mit den Alternativen:

| Weg | erreicht | Aufwand | Preis |
|---|---|---|---|
| **IMAP** | jedes Mailprogramm auf jedem Gerät | Wochen | muss irgendwo laufen (siehe unten) |
| Native iOS-App | iPhone/iPad | Wochen, plus zweite Oberfläche | App-Store-Prüfung, 15–30 % |
| MCP (gebaut) | Modelle | Tage | — |
| Signatur (in Arbeit) | senkt die Einstiegshürde | Tage | 99 USD/Jahr |

## Die eine Entscheidung, die alles bestimmt

**Wo läuft der IMAP-Server?** Davon hängt ab, ob das Versprechen „kein Server"
hält.

1. **Nur lokal** (`127.0.0.1:143`). Apple Mail auf demselben Mac funktioniert.
   Das Telefon nicht. Ehrlich, billig, halber Nutzen.
2. **Öffentlich erreichbar.** TLS, echte Anmeldung, ein Host, der läuft. Damit
   ist s3mail ein Dienst, den jemand betreibt — genau das, was es heute nicht
   ist. Das Bedrohungsmodell aus „Wer darf ran" fällt komplett.
3. **Privates Netz — der empfohlene Weg.** s3mail läuft weiter auf dem Rechner
   zu Hause, Tailscale oder WireGuard bringt das Telefon in dasselbe Netz.
   Apple Mail verbindet sich auf die private Adresse. Kein öffentlicher Port,
   kein Dienst zu betreiben, und das Telefon ist erreicht.

### Variante 3 im Alltag: wie das Telefon den Rechner erreicht

Die erste Frage, die jeder stellt, und sie ist berechtigt: *„Wie kommt das
iPhone an den Mac, wenn ich unterwegs bin?"*

Tailscale ist kein „gleiches WLAN", sondern ein eigenes Netz über das Internet.
Beide Geräte melden sich im selben Tailnet an und bekommen feste Adressen
(`100.x.y.z`). Von da an erreicht das Telefon den Rechner aus dem Zug, aus dem
Hotel, aus fremdem WLAN — direkt zwischen den Geräten, notfalls über ein Relais,
durchgehend verschlüsselt. In Apple Mail steht als Server der Name des Rechners.

Drei Dinge, die dafür anders sein müssen als heute:

- **Binden auf die Tailscale-Adresse**, nicht auf `0.0.0.0`. Sonst hängt der
  Server auch am Café-WLAN, und das ist genau das, was Variante 3 vermeiden
  will.
- **Ein Passwort je Postfach** in der Konfiguration (siehe Tabelle unten). Nicht
  die AWS-Zugangsdaten.
- **TLS**, weil Apple Mail auf iOS es sehen will. `tailscale cert` stellt für den
  Rechnernamen ein echtes Zertifikat aus; damit ist das erledigt, ohne
  öffentlichen Port.

**Der Haken, der die Variante wirklich begrenzt:** der Rechner muss an und wach
sein. Klappt der Deckel zu, ist das Postfach weg — kein Abruf, kein `IDLE`,
nichts. Ein Mailserver, der schläft, während man unterwegs ist, fehlt genau
dann, wenn man ihn braucht. Das ist kein Implementierungsdetail, sondern die
Eigenschaft, die über die Alltagstauglichkeit entscheidet.

| Weg | was er kostet |
|---|---|
| Rechner wach halten (`caffeinate`, Ruhezustand aus) | Strom — und ein zugeklappter Laptop hilft trotzdem nicht |
| **Kleiner Dauerläufer im Tailnet** — Raspberry Pi, Mac mini, NAS | ein Gerät mehr, aber es bleibt der eigene Rechner, kein Dienst |
| Öffentlich erreichbarer Server | das ist Variante 2: ein Dienst, den jemand betreibt |

Der Dauerläufer ist der ehrliche Weg für „unterwegs", und er ändert am Entwurf
nichts: dasselbe Programm, dieselbe Konfiguration, nur auf einer Kiste, die
durchläuft. Wer das nicht will, bekommt mit Variante 3 ein Postfach, das
verfügbar ist, solange der Rechner läuft — was für viele reicht, aber gesagt
gehört, bevor jemand sich darauf verlässt.

**Variante 3 zuerst bauen.** Sie verlangt technisch dasselbe wie 1 und macht
den Unterschied allein durch das Netz darunter. Variante 2 ist eine
Produktentscheidung, keine Ausbaustufe.

## Der harte Teil: UIDs

Alles andere an IMAP ist Fleißarbeit. Das hier ist die Stelle, an der der
Entwurf steht oder fällt.

IMAP verlangt pro Ordner **UIDs**: 32-Bit-Zahlen, streng aufsteigend, für immer
stabil. Ein Client merkt sich „ich habe bis UID 4711", fragt beim nächsten Mal
nach allem darüber und verlässt sich darauf, dass sich darunter nichts
geändert hat.

Das kollidiert mit dem Kern des Zustandsentwurfs. Aus `CLAUDE.md`:

> jeder Schreibvorgang bekommt seinen eigenen Schlüssel, `load()` spielt alle
> Ops über dem Wasserstand `upto` auf den Snapshot. Deshalb gibt es **keinen
> Konflikt und kein If-Match**

Eine UID zu **vergeben** ist aber genau das, was dieser Entwurf vermeidet: eine
Einigung darüber, wer die nächste Nummer bekommt. Zwei Rechner, die gleichzeitig
dieselbe neue Mail sehen, müssten sich abstimmen.

### Der Ausweg: ableiten statt vergeben

Das Op-Log hat bereits eine **totale Ordnung** — die Op-Namen sind so gebaut,
dass ihre lexikografische Reihenfolge der Schreibreihenfolge entspricht
(`store/state.go`, `opName`). Wer dieselben Ops in derselben Reihenfolge
einspielt, kommt zum selben Ergebnis. Genau darauf lässt sich eine UID-Vergabe
setzen, die keine Abstimmung braucht:

- Eine neue Op-Art `uid`: *„Basename M bekommt im Ordner F die Nummer N."*
- Beim Indexieren werden neue Mails **sortiert** (nach `LastModified`, bei
  Gleichstand nach Key) durchgegangen und bekommen fortlaufende Nummern ab dem
  bisherigen Höchststand des Ordners.
- `Apply` muss die Op idempotent und **konvergent** machen: existiert für M
  schon eine Nummer, gewinnt die aus dem Op mit dem kleineren Schlüssel. Beide
  Rechner kommen so zum selben Ergebnis, egal in welcher Reihenfolge sie die
  Ops sehen.

**Beim Bauen zeigte sich, dass der letzte Punkt billiger ist als gedacht.**
`Load` spielt die Ops in lexikografischer Schlüsselreihenfolge ein (`listOps`
sortiert, und `opName` stellt den Zeitstempel voran) — auf jedem Rechner
dieselbe. „Wer zuerst zuweist, gewinnt" *ist* damit schon „kleinerer Schlüssel
gewinnt", und `Apply` muss den Schlüssel gar nicht kennen. Die Regel bleibt rein,
so wie der Rest von `core`.

**Der Restfall, der ehrlich benannt gehört:** vergeben zwei Rechner gleichzeitig
dieselbe Nummer an verschiedene Mails, löst die Regel oben das zwar eindeutig
auf — aber ein Client, der die verlorene Zuordnung schon gesehen hat, hat dann
falsche Daten. Dafür gibt es das Sicherheitsventil:

### UIDVALIDITY ist das Ventil

IMAP kennt pro Ordner eine `UIDVALIDITY`. Ändert sie sich, wirft der Client
seinen ganzen Zwischenspeicher weg und lädt neu. Sie gehört in den Zustand und
muss steigen, wann immer die UID-Vergabe angefasst oder ein Konflikt aufgelöst
wurde.

Damit wird aus einem theoretisch unlösbaren Problem ein praktisch beherrschbares:
im schlimmsten Fall synchronisiert ein Client einmal neu.

## Was sonst zu bauen ist

| Was | Aufwand | Anmerkung |
|---|---|---|
| `BODYSTRUCTURE` | groß | Clients wollen den MIME-Baum, ohne den Körper zu laden. `mimeparse` liefert heute eine flache Sicht — der Baum muss dazu. |
| Teil-`FETCH` (`BODY[1.2]<0.1000>`) | mittel | Einzelne Teile ausschneiden. Der Body-Cache trägt das schon. |
| Flags | klein | `\Seen`, `\Flagged` auf den bestehenden Zustand; Keywords auf Tags. Passt eins zu eins. |
| `\Deleted` + `EXPUNGE` | Entscheidung | IMAP löscht in zwei Schritten. **Vorschlag:** `EXPUNGE` verschiebt in den Papierkorb statt zu löschen — s3mails Modell bleibt, und niemand verliert Mail durch ein Mailprogramm, das anders denkt. |
| `APPEND` | klein | Clients laden Gesendet-Kopien und Entwürfe hoch. `Mailbox.Put()` gibt es seit v0.8. |
| `IDLE` | klein | Der SQS-Empfänger ist da; `IDLE` hängt sich an dieselbe Benachrichtigung. |
| `SEARCH` | mittel | `core/search.go` deckt einen Teil ab. Die IMAP-Grammatik ist größer — der Rest lokal filtern. |
| Anmeldung | mittel | Ein Passwort je Postfach, in der Konfiguration. Nicht die AWS-Zugangsdaten. |

**Bibliothek:** `emersion/go-imap` v2 — derselbe Autor wie `go-message`, das
schon eine Abhängigkeit ist. Die Backend-Schnittstelle passt auf `store/`.

## Was das kostet

**Wochen, nicht Tage.** Der UID-Entwurf allein ist ein Nachmittag Denken und
mehrere Tage Umsetzung mit Tests, die zwei Rechner nachstellen — so wie
`TestTwoMachines` es heute für den Zustand tut.

Dazu **Interoperabilität**, und die wird regelmäßig unterschätzt: Apple Mail,
Thunderbird und Outlook halten sich unterschiedlich genau an die Spezifikation
und haben je eigene Marotten. Ein Server, der gegen einen Client funktioniert,
funktioniert nicht gegen drei. Das gehört als eigene Teststrecke eingeplant,
nicht als Abnahme am Ende.

## Was ausdrücklich nicht geplant ist

- **JMAP.** Moderner und angenehmer zu bauen, aber praktisch kein Client kann
  es. Der ganze Zweck ist, vorhandene Programme zu erreichen.
- **POP3.** Löst nichts, was IMAP nicht besser löst.
- **Ein öffentlich erreichbarer Dienst** (Variante 2 oben) — das wäre ein
  anderes Produkt mit einem anderen Bedrohungsmodell und gehört, wenn
  überhaupt, gesondert entschieden.

## Angehalten nach Schritt 1 (2026-08-25)

Der Ausbau ist **bewusst pausiert**, nicht steckengeblieben. Der Grund steht in
[`IOS.md`](IOS.md): das Ziel war „Postfach auf dem Telefon", und dafür ist die
native App der kürzere Weg — sie braucht keinen laufenden Rechner, IMAP schon.

Was IMAP darüber hinaus kann, bleibt richtig: jedes Mailprogramm auf jedem
Gerät, auch Thunderbird und Outlook am Schreibtisch. Das ist ein eigener Wunsch
und ein schwächerer, solange s3mail selbst der Schreibtisch-Client ist.

Was liegen bleibt, kostet nichts: die Nummerierung läuft nur auf Abruf, es gibt
keinen Server und keine Abhängigkeit. Wer hier weitermacht, fängt bei Schritt 2
an.

## Reihenfolge, wenn es losgeht

1. ~~UID-Entwurf festklopfen und **zuerst testen** — die Op-Art, die Konvergenz
   zweier Rechner, das UIDVALIDITY-Ventil.~~ **Gebaut am 2026-08-24.**
   `core/uid.go` trägt die Ops `uid` und `uidretire`, die reine Vergabe
   `AssignUIDs` und die Ordnerzustände (`validity`, `next`, `uids`). Geprüft
   sind: Vergabe in Reihenfolge, Wiedereinspielen ohne Wirkung, zwei Rechner mit
   derselben Nummer (einer gewinnt, der Verlierer bleibt unnummeriert und
   bekommt beim nächsten Durchgang eine frische Nummer, `UIDVALIDITY` steigt),
   eine zurückgegebene Nummer wird nie erneut vergeben, Ordner nummerieren
   unabhängig, und der Zustand übersteht Snapshot und Verdichtung.
   **Verdrahtet am 2026-08-25** (`store/uids.go`), aber **auf Abruf**:
   `EnsureUIDs(folder)` nummeriert, was noch keine Nummer hat — sortiert nach
   `LastModified`, bei Gleichstand nach Key, damit zwei Rechner dieselbe Op
   erzeugen. `Move` gibt die Nummer im Quellordner zurück, ohne den Höchststand
   zu senken. Geprüft ist auch das, worauf es ankommt: ein zweiter Durchgang
   nummeriert nichts um, und zwei Rechner am selben Bucket kommen zum selben
   Ergebnis.

   **Warum auf Abruf und nicht bei jedem Indexieren:** Nummern kosten rund
   15 Byte je Mail im Zustandsdokument, das jeder Rechner bei jedem Start
   herunterlädt — gemessen, nicht geschätzt: 5000 Mails ergeben 74 KB reine
   Nummerierung. Ein Postfach, dessen Besitzer nie IMAP anfasst, soll das nicht
   bezahlen. Ein IMAP-Server ruft `EnsureUIDs` beim `SELECT` auf, und damit ist
   es genau dann da, wenn es jemanden gibt, der es liest.
2. **Als Nächstes:** `BODYSTRUCTURE` in `mimeparse`, mit dem vorhandenen
   Testkorpus — der um die hässlichen Fälle gewachsen ist und damit genau der
   ist, den ein MIME-Baum braucht.
3. Minimaler Server: `SELECT`, `FETCH`, `STORE`, `SEARCH` gegen **Apple Mail**.
4. `APPEND`, `MOVE`, `EXPUNGE`.
5. `IDLE` auf die SQS-Benachrichtigung.
6. Interop gegen Thunderbird und Outlook.
7. Anleitung für Tailscale/WireGuard in die README.
