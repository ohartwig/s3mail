// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package s3fake

import "git.ole-hartwig.eu/development/s3mail/s3mail/go/store"

var _ store.S3 = (*Fake)(nil)
