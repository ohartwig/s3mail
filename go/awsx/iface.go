package awsx

import "s3mail/store"

var _ store.S3 = (*S3)(nil)
