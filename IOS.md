# Native App für iPhone und iPad

Aufgeschrieben, damit die Überlegung nicht zweimal gemacht werden muss.

**Stand 2026-08-25: Schritte 1 bis 4 sind gebaut**, im eigenen Repo
[`development/s3mail/ios`](https://git.ole-hartwig.eu/development/s3mail/ios).
Offen sind Push (5) und Verteilung (6) — beide hängen am Mac-Runner und an
Apple, nicht am Code. Was unten steht, gilt weiter; die Begründungen sind der
Grund, warum es so gebaut wurde.

Gegenstück zu [`IMAP.md`](IMAP.md). Die beiden schließen sich nicht aus, und
welcher Weg wofür taugt, steht unten.

## Warum überhaupt, wenn es IMAP gibt

IMAP erreicht mehr Geräte mit weniger Produkt — das bleibt richtig. Aber es hat
einen Haken, der sich nicht wegdiskutieren lässt: **irgendwo muss ein Server
laufen.** In der empfohlenen Variante 3 ist das der Rechner zu Hause, und ein
zugeklappter Laptop ist kein Mailserver. Wer unterwegs Post lesen will, braucht
dann einen kleinen Dauerläufer — ein Gerät mehr, das jemand pflegt.

**Eine native App hat diesen Haken nicht — wenn sie direkt mit AWS spricht.**
Dann gibt es keinen Server, den jemand betreibt, und das Versprechen von s3mail
gilt zum ersten Mal auch unterwegs. Das ist der ganze Grund, warum dieser
Entwurf existiert.

## Die eine Entscheidung, die alles bestimmt

**Womit redet die App?**

1. **Direkt mit AWS** — S3 für die Mail, SES zum Senden, KMS bei verschlüsselten
   Postfächern. Kein Server, kein Tunnel, kein wacher Rechner.
2. **Mit s3mail über Tailscale** — die vorhandene HTTP-API, das Telefon im
   selben Netz.

**Variante 1, sonst lohnt es nicht.** Variante 2 hat exakt dieselbe
Voraussetzung wie IMAP (der Rechner muss laufen), erreicht aber nur ein Gerät
statt jedes Mailprogramms. Wer bei 2 landet, soll IMAP bauen.

Der Zustand ist für Variante 1 bereits gebaut, und das ist kein Zufall: Tags,
gelesen/ungelesen, Stern und Regeln liegen als **Op-Log im Bucket**, ohne
Sperre, ohne `If-Match`. Zwei Schreiber sind vorgesehen. Das Telefon ist aus
Sicht des Zustands einfach ein zweiter Rechner — dieselbe Eigenschaft, die
`TestTwoMachinesNoConflict` seit v0.4 prüft.

## Der Kern der Sache: nicht zweimal dasselbe schreiben

Eine zweite Fassung von `mimeparse` in Swift wäre die eigentliche Katastrophe
dieses Vorhabens. MIME ist der Teil, an dem sich die hässlichen Fälle sammeln —
der Testkorpus hat aus gutem Grund Outlook-Multiparts, `winmail.dat`, drei
Zeichensätze in einer Betreffzeile und sechs Ebenen Verschachtelung. Das ein
zweites Mal richtig zu bekommen ist teurer als die ganze Oberfläche, und ab dann
gäbe es zwei Wahrheiten, die auseinanderlaufen.

**Deshalb: der Go-Kern kommt mit.** `gomobile bind` erzeugt aus einem Go-Paket
ein XCFramework, das Swift aufrufen kann. `core`, `mimeparse` und `store`
bleiben ein Quellbaum, und die App ist die Oberfläche darüber.

Was daran zu prüfen ist, bevor irgendetwas anderes gebaut wird:

- Läuft das AWS-SDK für Go unter iOS? Es ist reines Go über `net/http`, es
  spricht nichts dagegen — aber „spricht nichts dagegen" ist keine Messung.
- Wie groß wird das Paket? Die Go-Laufzeit kommt mit; ein paar Dutzend MB sind
  zu erwarten und für eine Mail-App verkraftbar.
- Wie fühlt sich die Brücke an? `gomobile` erzeugt eine Objective-C-nahe
  Schnittstelle; die Datentypen sind eingeschränkt (kein `map`, keine Generics),
  also braucht es eine schmale Fassade aus JSON-Zeichenketten oder Bytes.
- Wird `gomobile` noch gepflegt? Es ist ein Nebenprojekt des Go-Teams. Wenn es
  eines Tages stehenbleibt, ist die Frage, ob der letzte Stand weiter baut.

**Das ist der Spike, der als Erstes gemacht wird**, und er entscheidet über das
Vorhaben. Fällt er negativ aus, ist die Antwort nicht „dann eben Swift", sondern
„dann IMAP".

## Zugang: was auf dem Telefon liegt

Die schärfste Sicherheitsfrage, und sie hat eine klare Antwort.

**Nicht** derselbe Zugangsschlüssel wie auf dem Rechner. Ein verlorenes Telefon
zwänge sonst dazu, den Schlüssel überall zu tauschen — und niemand tut das
sofort.

**Sondern ein eigener IAM-Benutzer je Gerät**, mit derselben engen Policy, die
der Assistent ohnehin vorschlägt. Dann ist ein verlorenes Telefon ein Klick in
der IAM-Konsole und sonst nichts. Der Schlüssel liegt im Schlüsselbund des
Geräts, an den Gerätecode gebunden (`kSecAttrAccessibleWhenUnlockedThisDeviceOnly`),
also weder im iCloud-Backup noch auf einem zweiten Gerät.

**Eingerichtet wird auf dem Rechner, nicht auf dem Telefon.** Der Assistent kann
das schon: Bucket, Prefix und Absender aus der IAM-Policy lesen. Er zeigt am
Ende einen QR-Code, das Telefon liest ihn, fertig. Ein Einrichtungsdialog auf
einem Telefon, in dem jemand eine Bucket-Policy tippt, wäre das Gegenteil des
Maßstabs, den dieses Projekt hat.

## Push ohne Server

SES benachrichtigt ein SNS-Topic — das gibt es für den Push auf dem Rechner
schon. **SNS kann direkt an APNs zustellen**, ohne dass etwas dazwischen läuft.
Es braucht einen APNs-Schlüssel aus dem Apple-Developer-Konto (vorhanden, seit
der Signierung) und eine SNS-Platform-Application.

Damit bekommt das Telefon neue Mail gemeldet, ohne dass irgendwo ein Dienst
läuft. Das ist die Eigenschaft, an der IMAP-Variante 3 scheitert.

**Die Benachrichtigung trägt keinen Inhalt.** Kein Absender, kein Betreff, nur
„sieh nach" — ein stiller Push mit `content-available`. Die App holt danach
selbst und zeigt Absender und Betreff aus dem, was sie gelesen hat.

Das ist die eine Entscheidung, die hier vorab festgehalten werden muss, weil der
bequeme Weg der falsche ist. SNS kann den Betreff mitschicken, und es wäre eine
Zeile weniger Code. Nur läuft dann jeder Betreff über Apples Server — bei einem
Programm, dessen ganzer Zweck es ist, dass die Mail im eigenen Bucket bleibt und
kein Anbieter dazwischen sitzt. Ein Postfach, das Betreffzeilen an Apple
weitergibt, damit die Meldung hübscher aussieht, hat sein Versprechen gebrochen,
und niemand würde es merken.

Der Preis ist ehrlich zu nennen: eine stille Benachrichtigung darf iOS
verzögern, zusammenfassen oder ganz auslassen, wenn das Gerät sparsam sein will.
Wer die Mail sofort will, zieht herunter. Das ist der richtige Tausch.

### Was dafür noch fehlt

Nichts davon liegt im Code, und deshalb ist Schritt 5 offen:

1. **Ein APNs-Schlüssel** aus dem Apple-Developer-Konto (`.p8`, Key ID, Team ID).
2. **Eine SNS-Platform-Application** damit, plus je Gerät ein Endpoint.
3. **Eine Benachrichtigung, wenn eine Mail ankommt.** Der Push auf dem Rechner
   hängt an SES; ob der Bucket dafür ein S3-Event braucht oder das vorhandene
   SNS-Topic reicht, ist beim Einrichten zu prüfen.
4. **Zwei Rechte in der Geräte-Policy**: `sns:CreatePlatformEndpoint` und
   `sns:Subscribe`. Das Telefon meldet sich selbst an — sonst müsste jemand für
   jedes neue Gerät in die Konsole.

## Was die App nicht können soll

Derselbe Gedanke wie beim MCP-Server: der Zuschnitt ist die Entscheidung.

- **Keine Einrichtung.** Kein Bucket eintippen, keine Policy, keine Regionwahl.
- **Kein Regel-Editor.** Regeln laufen weiter, sie werden am Rechner geschrieben.
- **Kein endgültiges Löschen.** Papierkorb ja, `--no-delete` in Wirkung: was am
  Telefon verschwindet, ist zurückholbar.

Lesen, schreiben, senden, einsortieren, suchen. Das ist ein Postfach.

## Was es wirklich kostet — die Korrektur zu IMAP.md

`IMAP.md` führt für die native App „App-Store-Prüfung, 15–30 %" als Preis. **Das
stimmt so nicht**, und die Zahl hat die Entscheidung schiefer aussehen lassen,
als sie ist:

- Die 15–30 % sind Apples Anteil an **bezahlten Apps und In-App-Käufen**. Eine
  kostenlose App zahlt nichts.
- Die öffentliche Prüfung lässt sich umgehen, wenn die App nicht in den Store
  soll: **TestFlight** (bis 10 000 Tester, leichte Prüfung), **Ad Hoc** (100
  Geräte je Jahr, keine Prüfung), **Custom Apps** über Apple Business Manager
  für einzelne Kunden.
- Das Developer-Programm kostet 99 USD im Jahr und ist seit der Signierung
  ohnehin bezahlt.

Für den Eigenbedarf und eine Handvoll Kunden kostet die App also **nichts außer
Arbeit**. Was bleibt, ist der Aufwand — und der ist echt.

## Wo das liegt

Seit dem 2026-08-25 sammelt die Untergruppe **`development/s3mail`** alles, was
dazugehört:

    development/s3mail/
      s3mail    Go-Programm, MCP-Server und der Kern
      ios       diese App

Zwei Repositories und nicht eines, weil die Werkzeugketten nichts teilen: hier
Xcode und Swift, dort Linux, Docker und Kreuzbauen. Und weil der Go-Kern für die
App eine **Bibliothek** ist — das ist die ehrliche Beziehung, und sie hat einen
Namen: `git.ole-hartwig.eu/development/s3mail/s3mail`. Vorher hieß das Modul
schlicht `s3mail` und war von außen gar nicht einbindbar; ohne diesen Umzug
bliebe nur der Weg über ein gebautes XCFramework als Artefakt, und das ist genau
der Auslieferungssprung, der still fehlschlägt.

Ein Mac-Runner ist für die Pipeline nötig und heute nicht da (`mac-runner-01`
ist abgemeldet). Er wird für das Bauen der App wieder aktiviert — exklusiv dafür.

## Reihenfolge, wenn es losgeht

1. ~~**`gomobile`-Spike.**~~ **Gebaut.** Beide Risiken sind ausgeräumt: das
   XCFramework baut, und ein echter S3-Aufruf geht vom Simulator durch. Das war
   die Frage, an der alles hing.
2. ~~**Zugangsmodell**~~ **Gebaut.** `awsx.DevicePolicy` erzeugt die Policy je
   Gerät, der Assistent den QR — **ohne Schlüssel darin** —, das Telefon legt
   ihn in den Schlüsselbund.
3. ~~**Liste und Lesen**~~ **Gebaut.** Gegen das echte Postfach geprüft.
4. ~~**Schreiben und Senden**~~ **Gebaut.** Entwürfe liegen als richtige
   Nachrichten in `drafts/`, das Senden geht durch denselben Marker aus
   `store/sending.go` wie am Schreibtisch — auf dem Telefon wiegt der schwerer,
   weil iOS Apps im Hintergrund ohne Vorwarnung beendet.
5. **Push** über SNS an APNs. **Offen.**
6. **Verteilung**: TestFlight für den Anfang, Ad Hoc für Kunden. **Offen** —
   braucht den Mac-Runner.

### Was dabei nicht im Plan stand

- **Die Texte.** `core.Folder.Label` ist ein Katalogschlüssel, kein Wort: der
  Ordnername ist ein S3-Prefix und darf nie übersetzt werden. Am Schreibtisch
  löst `web/` das auf, auf dem Telefon zunächst niemand — die Seitenleiste zeigte
  `folder.sent`. Die App hat jetzt einen eigenen Katalog in `de`/`en`/`es` und
  einen Test dafür wie `i18n_test.go`. Die Brücke gibt **Codes** zurück, keine
  Sätze; ein Go-Kern in einer App hat keine Sprache zu haben.
- **Nachsichtiges Dekodieren.** `core.Message` trägt `omitempty`. Ein strenger
  Decoder ließ das Postfach leer und nannte dabei ein Feld, das niemand anzeigt.
  Pflicht ist nur der Key.

## Was ausdrücklich nicht geplant ist

- **Kein Android.** Dieselbe Frage noch einmal, mit anderem Werkzeug; erst wenn
  iOS steht und sich bewährt hat.
- **Kein Mac Catalyst.** Auf dem Mac gibt es s3mail schon.
- **Kein zweites Zustandsmodell.** Was die App tut, geht durch dasselbe Op-Log.
  Wer hier eine Abkürzung nimmt, baut den Konflikt ein, den das Op-Log gerade
  vermeidet.

## Verhältnis zu IMAP

Sie schließen sich nicht aus, und sie lösen nicht dasselbe Problem:

| | erreicht | braucht einen laufenden Rechner |
|---|---|---|
| **IMAP** | jedes Mailprogramm auf jedem Gerät | **ja** |
| **Native App** | iPhone und iPad | **nein** |

Wer beides hat, hat unterwegs das Telefon ohne Voraussetzung und am
Schreibtisch jedes Programm, das er mag. Wer eines zuerst bauen will und
unterwegs lesen möchte, baut die App — sie ist der einzige Weg, der ohne einen
Rechner auskommt, der wach bleibt.
