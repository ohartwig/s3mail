// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package awsx

import "git.ole-hartwig.eu/development/s3mail/s3mail/store"

var _ store.S3 = (*S3)(nil)
