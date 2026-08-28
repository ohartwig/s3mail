# Datenschutz

**Zuletzt gegen den Code geprüft: 28.08.2026.**

s3mail erhebt nichts, schickt uns nichts und hat keine eigenen Server. Diese
Seite ist kurz, weil es wenig zu sagen gibt — und das Wenige soll überprüfbar
sein statt beruhigend.

## Wohin Ihre Post geht

In einen S3-Bucket in **Ihrem eigenen** AWS-Konto, dorthin geschrieben von
Amazon SES. Das Schreibtisch-Programm und die iOS-App lesen ihn direkt, mit
einem Zugangsschlüssel, den Sie angelegt haben. Nichts läuft über einen Rechner
des Autors.

Das ist keine Absichtserklärung, sondern das, was das Programm kann. Es kennt
keine andere Adresse als die von AWS, und Sie können das nachsehen: der
Quelltext ist offen.

## Was die Programme speichern

**Auf dem Schreibtisch:** die Konfiguration in `~/.config/s3mail` unter Linux,
`~/Library/Application Support/s3mail` unter macOS, `%AppData%\s3mail` unter
Windows — und einen Zwischenspeicher der Mailtexte, verschlüsselt auf dem Weg
auf die Platte, weil ein Benutzerverzeichnis in Sicherungen und abgeglichenen
Ordnern mitreist. AWS-Schlüssel landen als benanntes Profil in
`~/.aws/credentials`, dort, wo jedes andere AWS-Werkzeug sie erwartet.

**Auf dem Telefon:** die Kennung des Postfachs — Bucket und Prefix — in den
Einstellungen der App, und den Zugangsschlüssel im Schlüsselbund, markiert als
`WhenUnlockedThisDeviceOnly` — er wandert also nicht in eine Sicherung und
erreicht kein zweites Gerät. Ein Zwischenspeicher für Index und Mailtexte liegt im Sandkasten der
App.

Beides liegt auf Ihren Geräten. Nichts davon wird irgendwohin geschickt.

## Benachrichtigungen

Wenn Sie sie einschalten, übergibt das Telefon dem Push-Dienst von Apple eine
Gerätekennung, und diese Kennung geht an Amazon SNS **in Ihrem eigenen
AWS-Konto**. Trifft Post ein, schickt SNS eine Benachrichtigung, die genau das
sagt: es ist etwas angekommen. Kein Absender, kein Betreff, kein Inhalt — mit
Absicht, denn eine Push-Nachricht läuft über Apples Server, und der ganze Sinn
dieser Anordnung ist, dass Ihre Post das nicht tut.

## Entfernte Bilder in Mail

Bleiben blockiert, bis Sie sie anfordern. Ein Mailprogramm, das sie lädt, gibt
jedem Absender eine Lesebestätigung: allein die Anfrage bestätigt, dass die
Adresse gelesen wird, ungefähr wann und ungefähr von wo. Sie können sie je
Nachricht laden; die Entscheidung wird für die nächste nicht gemerkt.

## Keine Auswertung

Kein Tracking, keine Telemetrie, keine Absturzberichte, keine Werbekennungen,
keine Fremdbibliotheken irgendwelcher Art. Die App spricht mit AWS und mit
Apples Push-Dienst, und mit sonst nichts.

## Ihre Rechte

Es liegen hier keine Daten von Ihnen in fremder Hand, also gibt es bei uns
nichts zu erfragen, zu berichtigen oder zu löschen. Was in Ihrem AWS-Konto
liegt, regelt Ihr Vertrag mit AWS; was auf Ihren Geräten liegt, steuern Sie
selbst.

## Kontakt

Kai Ole Hartwig — <mail@ole-hartwig.eu>
