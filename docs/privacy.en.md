# Privacy

**Last checked against the code: 2026-08-28.**

s3mail collects nothing, sends nothing to us, and has no servers of its own.
This page is short because there is little to say, and what little there is
should be verifiable rather than reassuring.

## Where your mail goes

Into an S3 bucket in **your own** AWS account, written there by Amazon SES. The
desktop program and the iOS app read it from that bucket directly, with an
access key you created. Nothing passes through any machine belonging to the
author.

That is not a promise about intent — it is what the program can do. It knows no
address other than AWS's, and you can check that: the source is public.

## What the apps store

**On the desktop:** the configuration in `~/.config/s3mail` on Linux,
`~/Library/Application Support/s3mail` on macOS, `%AppData%\s3mail` on Windows —
and a cache of message bodies, encrypted on the way to disk, because a home
directory travels in backups and synced folders. AWS keys go into
`~/.aws/credentials` as a named profile, the way every other AWS tool expects
them.

**On the phone:** the mailbox's identity — bucket and prefix — in the app's
preferences, and the access key in the keychain, marked
`WhenUnlockedThisDeviceOnly` — so it does not travel in a backup and does not
reach a second device. A cache of the index and
message bodies sits in the app's sandbox.

Both are on your devices. Neither is sent anywhere.

## Notifications

If you turn them on, the phone hands Apple's push service a device token, and
that token goes to Amazon SNS **in your own AWS account**. When mail arrives,
SNS sends a notification that says only that: something arrived. No sender, no
subject, no content — deliberately, because a push notification travels through
Apple's servers and the whole point of this arrangement is that your mail does
not.

## Remote images in mail

Blocked until you ask for them. A mail client that loads them hands every sender
a read receipt: the request alone confirms that the address is read, roughly
when, and roughly from where. You can load them per message; nothing remembers
that decision for the next one.

## No analytics

No tracking, no telemetry, no crash reporting, no advertising identifiers, no
third-party SDKs of any kind. The app makes network requests to AWS and to
Apple's push service, and to nothing else.

## Your rights

There is no data of yours in anybody else's hands here, so there is nothing to
request, correct or delete from us. What is in your AWS account is governed by
your agreement with AWS; what is on your devices you control directly.

## Contact

Kai Ole Hartwig — <mail@ole-hartwig.eu>
