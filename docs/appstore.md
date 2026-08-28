# App Store submission

Everything App Store Connect asks for, written down once so it is not invented
under time pressure at the upload dialog. Character limits are Apple's.

## Names and text

| Field | Limit | German | English |
|---|---|---|---|
| App name | 30 | `s3mail` | `s3mail` |
| Subtitle | 30 | `Deine Post in deinem Bucket` (27) | `Your mail in your bucket` (24) |

**Promotional text** (170, changeable without a review):

> DE — Post, die Amazon SES in deinen S3-Bucket schreibt, endlich lesbar. Kein Server dazwischen, kein fremdes Postfach. Zum Ansehen ein Beispiel-Postfach ohne AWS-Konto.

> EN — Mail that Amazon SES writes into your S3 bucket, finally readable. No server in between, no mailbox on somebody else's machine. A sample mailbox to look at first.

**Keywords** (100, comma-separated, no spaces):

```
DE  s3,ses,aws,mail,postfach,imap,bucket,selbstgehostet,datenschutz,mime,e-mail
EN  s3,ses,aws,mail,mailbox,imap,bucket,self-hosted,privacy,mime,email,client
```

## Description

**German**

> s3mail liest E-Mail, die Amazon SES als rohes MIME in einen S3-Bucket schreibt — einen Bucket in deinem eigenen AWS-Konto.
>
> Dazwischen steht kein Server. Keiner, der ausfällt, keiner, den jemand betreibt, keiner, der mitliest. Die App spricht direkt mit S3 und SES, mit einem Zugangsschlüssel, der nur diesem Gerät gehört.
>
> ERST ANSEHEN, DANN EINRICHTEN
> Ein Beispiel-Postfach liegt in der App: erfundene Post, kein AWS-Konto nötig, keine Kopplung. Damit sichtbar ist, worum es geht, bevor irgendetwas eingerichtet wird.
>
> WAS SIE KANN
> • Ordner, Suche, Schlagworte, Stern, gelesen/ungelesen
> • HTML-Mail in einer abgeschotteten Ansicht; entfernte Bilder bleiben blockiert, bis du sie anforderst
> • Anhänge ansehen und sichern
> • Antworten, weiterleiten, schreiben — über SES, als deine verifizierte Adresse
> • Mehrere Postfächer auf einem Gerät
> • Benachrichtigung bei neuer Post, ohne dass Absender oder Betreff das Gerät verlassen
>
> WIE DAS TELEFON HINEINKOMMT
> Am Schreibtisch einen Namen eintippen, den QR-Code scannen, sechs Ziffern bestätigen. Dahinter entsteht ein eigener IAM-Benutzer mit einer Rechtegrenze — ein verlorenes Telefon ist eine einzelne Sperrung. Der Schlüssel reist versiegelt im Code, die PIN nicht: ein Foto des Bildschirms ist deshalb kein Zugang.
>
> WAS SIE NICHT TUT
> Keine Auswertung, keine Telemetrie, keine Absturzberichte, keine Werbekennungen, keine Fremdbibliotheken. Die App spricht mit AWS und mit Apples Push-Dienst — mit sonst nichts.
>
> VORAUSSETZUNGEN
> Ein AWS-Konto mit einem Bucket, in den SES schreibt, und das s3mail-Programm für den Schreibtisch, das dieses Gerät koppelt. Beides ist quelloffen (Apache-2.0).

**English**

> s3mail reads email that Amazon SES writes into an S3 bucket as raw MIME — a bucket in your own AWS account.
>
> Nothing sits in between. No server to fail, none for anyone to run, none to read along. The app talks to S3 and SES directly, with an access key belonging to this device alone.
>
> LOOK FIRST, SET UP LATER
> A sample mailbox ships with the app: made-up mail, no AWS account, no pairing. So you can see what this is before setting anything up.
>
> WHAT IT DOES
> • Folders, search, tags, starring, read/unread
> • HTML mail in a sandboxed view; remote images stay blocked until you ask for them
> • View and save attachments
> • Reply, forward, compose — through SES, as your verified address
> • Several mailboxes on one device
> • A notification when mail arrives, without sender or subject leaving the device
>
> HOW THE PHONE GETS IN
> Type a name on the desktop, scan the QR code, confirm six digits. Behind it an IAM user of its own is created, capped by a permissions boundary — so a lost phone is a single revocation. The key travels in the code, sealed; the PIN does not, which is why a photograph of the screen is not access.
>
> WHAT IT DOES NOT DO
> No analytics, no telemetry, no crash reporting, no advertising identifiers, no third-party SDKs. The app talks to AWS and to Apple's push service, and to nothing else.
>
> REQUIREMENTS
> An AWS account with a bucket SES writes into, and the s3mail desktop program, which pairs this device. Both are open source (Apache-2.0).

## URLs

| | |
|---|---|
| Support | `https://ole-hartwig.eu/open-source/s3mail/support` |
| Privacy | `https://ole-hartwig.eu/open-source/s3mail/datenschutz` |
| Marketing | `https://ole-hartwig.eu/open-source/s3mail` |

English visitors get `/en/open-source/s3mail/…`. **All three are hidden until
the pages are published** — a submission with a 404 privacy URL is rejected on
sight.

## Review notes

This is the part that decides whether review goes smoothly, because a reviewer
cannot pair a device: that needs an AWS account and the desktop program.

> This app reads mail from an Amazon S3 bucket belonging to the user. Setting up a real mailbox requires an AWS account and our desktop application, so no demo account can be provided — and none is needed.
>
> **To review the app, tap "Look at a sample" on the first screen.** It opens a sample mailbox that ships with the app: folders, HTML mail, an attachment. No account, no network, no pairing. Everything the app does with real mail can be seen there, except sending, which is disabled for the sample because there is nowhere to send to.
>
> The app collects no data and contacts no servers of ours. It talks to Amazon Web Services in the user's own account, and to Apple's push service if notifications are enabled.

## Category, rating, compliance

**Category:** Productivity (primary). Business as secondary if a second is wanted.

**Age rating:** 4+ is the expected answer. The app shows the user's own mail and
is not a platform for user-generated content, so the moderation and reporting
requirements of Guideline 1.2 do not apply. Note for the questionnaire: HTML mail
renders in a sandboxed web view with no navigation and no address bar, so this is
**not** "unrestricted web access".

**Export compliance — declared.** The app uses encryption:

| Where | What |
|---|---|
| Every AWS call | TLS, through the AWS SDK |
| The pairing code | PBKDF2-SHA256 (600,000 rounds) and AES-256-GCM, from Go's standard library |
| Client-side encrypted mail | AES-GCM and AES-CBC, plus KMS for the wrapped key |
| The keychain entry | iOS, `WhenUnlockedThisDeviceOnly` |

All of it is standard algorithms — nothing proprietary and nothing home-grown,
so the exemption applies. `INFOPLIST_KEY_ITSAppUsesNonExemptEncryption = NO` is
set in both configurations of the app target, which stops App Store Connect
asking at every upload. The table above is what the declaration rests on; keep
it current if the crypto ever changes.

One trap, because the Info.plist is generated rather than checked in: for keys
Xcode does not know the type of, an `INFOPLIST_KEY_` setting can land in the
plist as the **string** `"NO"` rather than a boolean, and App Store Connect
reads a string as no declaration at all — silently. Verify against the built
app, not the build settings:

```bash
plutil -extract ITSAppUsesNonExemptEncryption raw -expect bool \
    "$(xcodebuild -project ios-app/s3mail.xcodeproj -scheme s3mail \
        -showBuildSettings 2>/dev/null | awk '/ BUILT_PRODUCTS_DIR/{print $3}')/s3mail.app/Info.plist"
```

## Screenshots

Required: one set for the 6.9" iPhone. Others are derived by Apple.

Worth showing, in this order: the folder list with unread counts, a message
list, an open HTML message, the pairing screen, the mailbox switcher.

All of them can come from the sample mailbox — which is the second reason it
exists. Nobody has to photograph real correspondence.

## What is still needed and belongs to nobody but Apple

- A production APNs platform application (`APNS`, not `APNS_SANDBOX`) and a
  distribution provisioning profile. The code already reads `aps-environment`
  from the embedded profile, so nothing changes there — it has simply never run
  against production.
- Distribution certificate, App Store Connect record, the build upload itself.
