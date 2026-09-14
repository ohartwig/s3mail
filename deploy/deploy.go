// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

// Package deploy carries the CloudFormation template that sets up a mailbox
// from nothing.
//
// s3mail works out bucket, prefix and sender from the IAM policy of its access,
// which is the nicest thing about setting it up: nobody types any of it. What
// it cannot do is *create* that policy - and until this template existed, that
// left a gap somebody had to cross with a console and a lot of reading. A stack
// that anybody can launch in their own account closes it.
//
// The template is JSON and not YAML on purpose. YAML would read better, and it
// would need a third-party parser, which this project does not take. JSON is
// what the standard library can read - and that is what lets a test hold the
// template against the code that has to understand what it creates. A template
// nobody checks drifts, and it drifts silently: it keeps deploying, and what it
// deploys stops being what s3mail expects.
package deploy

import _ "embed"

// Template is the CloudFormation template, as it should be uploaded.
//
//go:embed s3mail.json
var template string

// Template returns the CloudFormation template for a single mailbox.
func Template() string { return template }
