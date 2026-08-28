# s3mail

A mail client for mail that Amazon SES writes into an S3 bucket you own.

There is no server in between. SES drops the raw message into your bucket, and
s3mail — a single binary with no runtime — starts a web server on `127.0.0.1`
and shows you the mailbox in your browser. The bucket is yours, the mail is
yours, and the only thing between the two is a program you can read.

There is an iOS app as well. It talks to the same bucket directly, with a key of
its own.

> **Documentation:** the full operational reference — IAM in detail, encrypted
> buckets, the state model, limits — is in [`README.de.md`](README.de.md), in
> German. Translating it is on the list; this page is the short way in.

## Setting it up

You need a domain SES receives for, and a bucket with an IAM user narrow enough
to be the only thing that reads it. A CloudFormation template creates the second
part:

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

See [`go/deploy/README.md`](go/deploy/README.md) for the two steps a template
cannot finish, and for what each resource is there to do.

Then start s3mail and enter the access key. That is the only thing it asks for:
bucket, prefix and sender all come out of the IAM policy of the key itself, so
there is nothing else to type and nothing to get wrong.

## Building

```bash
cd go
go build ./cmd/s3mail        # one file, about 15 MB
go test ./...                # no AWS access needed
```

Cross compiling works because CGO is off:

```bash
GOOS=windows GOARCH=amd64 go build ./cmd/s3mail
```

Releases carry builds for macOS (arm64 and amd64), Linux and Windows, with
checksums and a signature.

## How it fits together

Bottom to top; nothing below knows anything about what is above it.

**`mimeparse`** turns MIME into headers and parts. It reads only the head for the
index and the whole message when one is opened.

**`core`** is the logic without side effects: state and its operations, folder
names, the rule engine, search. No S3, no HTTP, no files.

**`store`** puts S3 behind an interface of six methods. Listing the bucket
fetches only the head of each new message with a range request, and compares
ETags. Folders are real S3 prefixes, and the message id is the basename — which
is why moving a message does not lose what you knew about it.

**State is shared without a lock.** Tags, read, starred and rules live in the
bucket as an operation log: every write gets its own key, and loading replays
them over a snapshot. So there is no conflict to resolve and no compare-and-swap
to get wrong — at the price that every operation has to be idempotent.

**`web`** is the interface: three HTML pages, vanilla JavaScript, no build step.
HTML mail renders in a sandboxed iframe with a content security policy, and
remote images are blocked until you ask for them.

**`mcp`** exposes the mailbox to a language model over stdin and stdout — read,
search, tag, move, draft. **Not send.** An incoming message is a stranger's text
inside the model's context, and the drafts folder is where a human looks before
anything leaves.

## Security

Three checks guard the local server: the `Host` header against DNS rebinding,
the `Origin` header against cross-site requests, and a token against anybody
else on the same machine.

The bucket policy denies reading and listing to every principal except the
mailbox and the devices it has paired — an explicit deny beats every allow,
including `AdministratorAccess`. Whoever owns the bucket can still change that
policy; what it buys is that doing so is a deliberate act rather than a glance.

A paired phone gets an IAM user of its own, capped by a permissions boundary, so
losing a phone is one revocation and not a rotation everywhere.

Found something? Write to <mail@ole-hartwig.eu>.

## Licence

Apache-2.0. The full text is in [`LICENSE`](LICENSE), attribution in
[`NOTICE`](NOTICE), and every source file carries the SPDX identifier.

The patent clause in section 3 is the reason for Apache-2.0 over MIT: it grants
every user the patent rights to what is in here, and takes them away again from
anyone who sues over them.
