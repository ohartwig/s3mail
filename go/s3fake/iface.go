package s3fake

import "s3mail/store"

var _ store.S3 = (*Fake)(nil)
