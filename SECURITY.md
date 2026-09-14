<!--
SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
SPDX-License-Identifier: Apache-2.0
-->

# Security policy

## Reporting a vulnerability

Please do not open a public issue or pull request for a security problem.
Send a report to <security@ole-hartwig.eu>; a PGP key for attachments is
published at <https://ole-hartwig.eu/.well-known/openpgpkey> (RFC 9580).

You will get an acknowledgement within 72 hours on business days and a
triage result within 7 days. Findings stay embargoed until a fix is released,
90 days after first response at the latest, and the reporter is credited
unless they prefer not to be.

## What counts

In scope: the `s3mail` binary built from this repository, the CloudFormation
template under `deploy` and the IAM policies the setup wizard writes — in
particular anything that lets a mail in the bucket run script or load a
remote resource in the browser (the HTML view is sandboxed and images are
blocked on purpose), reach the local server without its token, read a
mailbox beyond the prefix an IAM user was scoped to, or make a message leave
twice or not at all while the send marker says otherwise. The threat model
behind those choices is in the READMEs; a report that shows one of the
assumptions wrong is a security report even without an exploit.

Out of scope: AWS itself (S3, SES, KMS, IAM — report those to Amazon), the
browser, and denial of service against a server that only ever listens on
`127.0.0.1`.

The iOS app has its own repository and the same policy:
<https://github.com/ohartwig/s3mail-ios>.

## Supported versions

The latest minor release. Fixes are released as patch versions and noted in
the changelog with a `security` mention.

## Signed releases

Every release ships `SHA256SUMS` with a cosign signature; the macOS packages
are signed with a Developer ID and notarised. A package whose checksum is not
in a signed manifest did not come from the release pipeline.
