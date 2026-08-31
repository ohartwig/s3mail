# s3mail

A mail client for mail that Amazon SES writes into an S3 bucket you own.

There is no server in between. SES drops the raw message into your bucket, and
s3mail — a single binary with no runtime — starts a web server on `127.0.0.1`
and shows the mailbox in your browser. The bucket is yours, the mail is yours,
and the only thing between the two is a program you can read.

There is an iOS app as well. It talks to the same bucket directly, with an IAM
user and a key of its own.

> **Also in German:** [`README.de.md`](README.de.md) is the longer version and
> goes further in places — the console click-path for setting up SES by hand,
> and more on encrypted buckets. This page covers the same ground in less space.

## Requirements

### On the machine that runs s3mail

Nothing. No web server, no database, no Docker, no runtime. Download the package
for your platform — macOS (Apple Silicon and Intel), Linux, Windows — and run it.

The macOS packages are signed and notarised, so no `xattr` and no right-click
dance. Windows packages are not signed; SmartScreen will say so. Every platform
ships `SHA256SUMS` with a cosign signature.

Building it yourself needs Go 1.27 or newer. Nothing else — CGO is off.

Outbound, s3mail talks to `s3.<region>.amazonaws.com`,
`email.<region>.amazonaws.com` and, for encrypted mail,
`kms.<region>.amazonaws.com`. Nowhere else.

### In the AWS account

| What | For | Without it |
|---|---|---|
| A **domain verified in SES** | receiving at all | no mail arrives |
| An **MX record** pointing at SES | routes mail to AWS | mail goes to the old server |
| An **S3 bucket** in the same region | where the mail lands | — |
| A **bucket policy** for SES | lets SES write | mail is rejected |
| An **active receipt rule set** with an S3 action | writes the mail into the bucket | mail is accepted and thrown away |
| An **IAM identity** with the policy below | s3mail reads and sorts | s3mail cannot reach the bucket |
| A **verified sender address** | writing, replying, forwarding | reading works, sending does not (`--no-send`) |
| A **KMS key** (optional) | only for encrypted mail | see [Encrypted buckets](#encrypted-buckets) |

No Lambda, no EC2, no VPC, no WorkMail organisation.

## Setting it up

The bucket, the IAM user, the boundary for paired devices and the SES receipt
rule are what a CloudFormation template creates:

```bash
aws cloudformation create-stack \
  --stack-name s3mail \
  --template-body file://go/deploy/s3mail.json \
  --capabilities CAPABILITY_NAMED_IAM \
  --parameters \
      ParameterKey=MailDomain,ParameterValue=example.org \
      ParameterKey=MailboxLocalPart,ParameterValue=you \
      ParameterKey=MailBucket,ParameterValue=your-unique-bucket-name
```

Two things a template cannot finish, and each is one command: activating the
rule set — there is no CloudFormation resource for it — and minting an access
key. The template deliberately creates none: a key in a stack output is readable
by anyone who can read the stack, and stays that way.

```bash
aws ses set-active-receipt-rule-set --rule-set-name s3mail-you
aws iam create-access-key --user-name s3mail-you
```

See [`go/deploy/README.md`](go/deploy/README.md) for what each resource is there
to do. Setting the same thing up by hand in the console is written out step by
step in [`README.de.md`](README.de.md).

Then start s3mail and enter the access key. That is the only thing it asks for:
**bucket, prefix and sender all come out of the IAM policy of the key itself.**
Nothing else to type, and nothing else to get wrong.

The setup assistant then checks, in order, that it can list the bucket, read a
message, write, delete, share state between machines, decrypt, and send as the
verified address — and for every failing check it names the IAM action that is
missing.

## Who may access it

s3mail binds to `127.0.0.1` and has no users. Three checks guard every request,
each against a different attack:

- **A token** against anybody else on the same machine. It is in the address
  printed at startup, and without it the server does not answer. Serving a page
  sets it as a `SameSite=Strict` cookie, which is why attachment and `.eml`
  downloads work without carrying the token in every URL.
- **The `Host` header** against DNS rebinding. A foreign domain pointing at
  `127.0.0.1` would be the same origin as s3mail to the browser — but it sends
  its own name in `Host`, and that does not match.
- **The `Origin` header** against CSRF. A foreign page can `fetch()` a POST here
  without a preflight. It cannot read the answer, but deleting and sending would
  still happen. The browser sets `Origin` on exactly those requests.

**This is not a login.** Whoever has the address with the token sees the whole
mailbox. That is enough for a program running next to the browser on your own
machine, and it does not carry further: pointing `--host` at a public address
means putting the mailbox on the internet, and then it belongs behind a reverse
proxy with real authentication. Note that the `Host` check then switches itself
off — it only makes sense on loopback — so two of the three are gone.

AWS credentials live in `~/.aws/credentials` as a named profile, never in
s3mail's own configuration.

## Folders are real S3 prefixes

`--prefix mail/` is the root. What sits directly under it is the inbox;
subfolders are mail folders:

```text
mail/                     ← the prefix from the SES receipt rule = inbox
mail/archiv/              ← the prefix is the German word, and it is real
mail/spam/
mail/trash/
mail/sent/                ← a copy of everything sent
mail/drafts/              ← drafts, with Bcc and attachments
mail/.s3mail-state.json   ← tags, read/unread, starred, rules (snapshot)
mail/.s3mail-state/       ← the individual changes since that snapshot
```

Moving is `CopyObject` + `DeleteObject`. So you see the folders in the S3
console, and the trash can be emptied by a lifecycle rule.

The message id is the **basename**, not the full key — which is why everything
you knew about a message survives moving it between folders.

## What it does

**Reading.** Indexing fetches only the first 64 KB of each message with a range
request — enough for headers and a preview — and compares ETags, eight requests
in parallel. HTML mail renders in a `sandbox=""` iframe with a content security
policy; remote images are blocked until you ask for them, because loading them
hands every sender a read receipt. Attachments download individually, the raw
message as `.eml`. Images and PDFs open in a preview; everything else only
downloads — **SVG explicitly included**, because it looks like an image and is
XML with script support.

SPF, DKIM and DMARC are read from `Authentication-Results`, and only from the
topmost header that names `amazonses.com`: that header is ordinary text in an
ordinary message, so any sender can write themselves one that says `dkim=pass`.
A mark appears when one of the three **failed** — not a tick on every message,
because a mark that is always there is one nobody looks at.

**Writing.** Reply, forward, compose. Replies hang on the original thread via
`In-Reply-To` and `References`. Signatures sit above the quote, and go into the
text box rather than being added at send time, so they can still be shortened.
Addresses are suggested from the index while you type — not from an address
book, because a second place for addresses is a second place for them to be
wrong. Bcc travels in the recipient list to SES and never in the message header.

**No double send after a crash.** Before SES is asked, s3mail writes a marker
next to the draft and clears it only once the copy and the draft are done. If
the program dies in between, the next start finishes what is certain; where it
cannot tell whether SES took the message, a banner asks — rather than quietly
sending it a second time.

**Sorting.** Folders, trash, spam, archive. Tags with colours, several per
message. Read/unread with per-folder counts, starred. Multi-select with
shift-click, and a checkbox that selects **every hit of the current search** —
"everything from news@shop.io into the archive" is two clicks. If one message
fails, the rest still move and the message says how many actually arrived.

**Rules.** "Contains X in From/To/Subject/anywhere → folder + tags", applied to
new mail while indexing, first match wins. A message is auto-filed only once, so
moving it back makes it stay. Trash and spam are never touched by automation.

**Rule suggestions.** The rule dialog shows what you already do by hand: "you
moved 11 of 12 messages from *news@shop.io* into *Promotions*". It rests on
evidence, not taste — at least four messages from the same sender and at least
80 % of them in the same folder, with the numbers shown so you can check them.
Sender, folder and tags are read; **never the content of a message**, and with
no network call and no API key.

**Search.** Free words across From/To/Subject/preview/tags, plus filters:

```text
invoice from:client@x.com subject:"Quote" after:2026-01-01 before:2026-08-01
has:attachment has:spam is:unread is:starred tag:important in:archive
```

Saved filters live **in the bucket**, like tags and rules — so the second
machine sees them, and nothing is lost when somebody clears a browser cache.

**Waiting for a reply.** A sidebar filter: what went out at least five days ago
and got no answer, longest wait first. Answered is decided **by address, not by
thread** — a reply often arrives as a new message, from a colleague of the
person written to, or with a thread a ticket system has cut off.

**Keyboard.** `n` compose · `j`/`k` move · `x` select · `s` star · `e` archive ·
`Del` trash · `/` search.

**Language.** German, English and Spanish, chosen in the mailbox or by
`Accept-Language`. Search keywords work in all three: `from:` is `von:` is `de:`.
Folder names are real S3 prefixes and are shown, not translated.

## Multiple mailboxes

One s3mail serves any number of mailboxes — `info@` and `support@` in the same
bucket, or mailboxes in different AWS accounts. Each has its own sender,
signature and state. If one fails to open, the others still come up and the
message names the one that did not.

## Reachable for a model: `--mcp`

s3mail speaks MCP over stdin and stdout, so a language model can work in the
mailbox: read, search, tag, move, and write drafts.

**Not send.** That cut is the security decision, not an oversight. An incoming
message is a stranger's text arriving in the model's context, and the drafts
folder is the place where a human looks before anything leaves.

## Encrypted buckets

Two kinds, and only one of them is work.

**Server-side (SSE-S3 / SSE-KMS)** is the bucket's own encryption. S3 decrypts on
`GetObject` and s3mail notices nothing. SSE-KMS needs `kms:Decrypt` on the key,
and `kms:GenerateDataKey` for writing. Moving a message reads the original's
encryption with `HeadObject` and passes it to `CopyObject`, so the copy does not
silently land under the bucket default. Storage class travels the same way.

**Client-side** — the KMS option in the SES receipt rule — means SES encrypts
*before* the message reaches S3. The bucket then holds an envelope, not MIME: the
data key wrapped by KMS in the object metadata, the content under AES-256. A
plain `GetObject` returns gibberish. s3mail recognises this from the metadata and
opens it, passing the encryption context from `x-amz-matdesc` to `kms:Decrypt` —
without it KMS refuses.

Two consequences: the index can no longer fetch parts with range requests,
because half a ciphertext does not decrypt, so objects are fetched whole; and
moving leaves the envelope untouched. A mixed mailbox is fine — it is decided per
object, and a message that cannot be decrypted fails alone.

## Shared state without a lock

Tags, read/unread, starred and rules live in the bucket so several machines see
the same thing. What gets written is not the whole document but **the single
change**: each write puts a small object under `<prefix>.s3mail-state/` on a key
only that write touches.

So two machines cannot collide. There is no shared key both point at, and
therefore no lock, no `If-Match` and no `412`. Marking a message read costs a few
hundred bytes instead of the whole mailbox.

Reading replays the snapshot plus every change newer than its watermark, in key
order — the keys start with a timestamp, so they sort themselves. Past fifty
open changes it compacts into a new snapshot.

The watermark is what makes this forgiving: if a cleanup leaves an object behind
because the delete failed, the next load skips it rather than applying it twice.
Deleting is refuse collection, not bookkeeping.

Two things worth knowing: **the clock decides the order**, which matters only for
changes that build on each other; and **all machines should run the same
version**, because an older one reads the snapshot and misses the changes beside
it. Without write permission, s3mail falls back to local state and says so.

## On the phone

A native iOS app reads the same bucket, with an IAM user of its own and a
permissions boundary that caps it at that mailbox's prefix. Pairing is: type a
name on the desktop, scan a QR code, confirm six digits — the key travels in the
code, sealed, and the PIN does not, which is why a photograph of the screen is
not access.

Losing a phone is a single revocation, not a rotation everywhere.

## Limits

- **No IMAP.** A normal mail program cannot open the mailbox. Sending would work
  over the SES SMTP endpoint; reading would not, because SES has no mailbox
  service. Why it is hard, and the way through, is in [`IMAP.md`](IMAP.md).
- **No thread grouping.** The list shows single messages. Proper threading needs
  `References`, subject normalisation and a way to deal with threads a ticket
  system cut off. What usually serves the purpose is there already: **History**
  in the message view shows everything with that address, across all folders.
- Push needs a bell in AWS: SES notifies an SNS topic that writes into an SQS
  queue s3mail watches. Without it, `--refresh` polls every 60 seconds by
  default. The queue is read out of the IAM policy — nothing to configure.
- Access hangs on a token, not on users. See [Who may access it](#who-may-access-it).
- Moving copies the object. Encryption and storage class come along; the version
  history of a versioned bucket does not.
- Very large mailboxes (> ~50k objects) take a while to index the first time.
  Then work with a narrower prefix.
- **What is on disk is encrypted** — AES-256-GCM with a key that is *not* in the
  home directory but in the system keychain: the login keychain on macOS, DPAPI
  bound to the Windows account, the Secret Service on Linux. A backup, a synced
  folder or a disk without FileVault is then worth nothing to anyone — the data
  is there, the key is not. Where there is no keychain, s3mail **does not start**
  but asks for a decision: `--no-cache` or `--cache-plaintext`. A silent
  plaintext cache would undo exactly the promise an encrypted bucket makes.

## Building

```bash
cd go
go build ./cmd/s3mail        # one file, about 15 MB
GOOS=windows GOARCH=amd64 go build ./cmd/s3mail
```

Cross compiling works because CGO is off.

## Tests

```bash
cd go && go test ./...
```

Sixteen packages, no AWS access: `s3fake` reproduces S3 with ETags, prefix
listing, object metadata and server-side encryption. Covered are the index,
folders, moving with its state hand-over, trash rules, tags, the rule engine,
search, send headers, state across machines (operations, compaction, watermark),
KMS decryption in both GCM and CBC, MIME parsing, configuration, the setup
checks, the HTTP layer, access control and error translation.

One group on its own:

```bash
go test ./store/ -run TestMoveCarriesTheState -v
```

## Security

The reporting path, the assumptions and the threat model — including what is
**not** covered — are in [`SECURITY.md`](SECURITY.md). Short version:
vulnerabilities to <security@ole-hartwig.eu>, not as an issue.

## Licence

Apache-2.0. The full text is in [`LICENSE`](LICENSE), attribution in
[`NOTICE`](NOTICE), and every source file carries the SPDX identifier.

The patent clause in section 3 is the reason for Apache-2.0 over MIT: it grants
every user the patent rights to what is in here, and takes them away again from
anyone who sues over them.
