<!--
SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
SPDX-License-Identifier: Apache-2.0
-->

# Contributing

Thank you for looking at this. s3mail is one binary that reads a bucket; most
contributions are a mail that was parsed wrongly, a test with that mail in it,
and the lines that make the test pass.

## Where to send what

- **Bugs and ideas:** issues on [GitHub](https://github.com/ohartwig/s3mail/issues).
- **Changes:** pull requests on GitHub are welcome. Development and the
  release pipeline run on the author's GitLab; a pull request is reviewed on
  GitHub and lands there through the mirror, so a merge may take a day.
- **Security problems:** see [SECURITY.md](SECURITY.md), not an issue.
- **The iOS app:** has its own repository,
  <https://github.com/ohartwig/s3mail-ios>.

## Setup

Any Go 1.27 toolchain. Nothing else: CGO is off, the tests run against
`s3fake` and need no AWS account, and there is no linter beyond `go vet` and
`gofmt`. The author's own checkout carries agent guidance and pipeline
material that are not part of the public repository.

```sh
go build ./cmd/s3mail
go test ./...
```

## Commits

Conventional Commits — the release is cut from them: `feat`, `fix`, `docs`,
`refactor`, `perf`, `test`, `ci`, `chore`; a breaking change takes `!` after
the type or a `BREAKING CHANGE:` footer. Only `feat`, `fix` and `perf`
produce a release. The body says *why*; where a decision was made against an
alternative, it names the alternative.

Identifiers, comments and test messages are English. The author's own
commit messages are German; yours may be either.

## What a change brings with it

- **A test that fails without it.** The MIME corpus under `mimeparse`
  and the S3 double in `s3fake` are there so that every behaviour can be
  pinned down without a network.
- **No sentence a user sees inside Go code.** Every user-facing text is a
  key in `i18n/locales`, in all three languages (`de`, `en`, `es`);
  `i18n_test.go` fails otherwise.
- **A field on `core.Message` raises `store.indexVersion`.** `Refresh()`
  only fetches what changed its ETag; an old index entry would keep the
  empty field forever.
- **Nothing new in the dependency list.** Standard library, the AWS SDK and
  `go-message` — a fourth dependency is a decision, not a convenience, and
  the pull request says why.

Before pushing:

```sh
gofmt -l . && go vet ./... && go test ./...
```

## What not to add

The limits in the README are decisions, not gaps: no server between SES and
the bucket, no IMAP, no deleting from the phone, no second encryption layer
where the platform already provides one. A change that adds one of those is a
conversation first, an issue, and a pull request last.
