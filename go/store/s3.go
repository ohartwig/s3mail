// Package store holds everything that talks to S3: the index, moving, and the
// persistence of the state as an op log. Access runs through a narrow interface,
// so the layer stays checkable without AWS.
package store

import (
	"context"
	"errors"
	"time"
)

// ObjectInfo is one entry from the listing.
type ObjectInfo struct {
	Key          string
	ETag         string
	Size         int64
	LastModified time.Time
}

// Object is a fetched object with its metadata (the crypto envelope sits there).
type Object struct {
	Body []byte
	ETag string
	Meta map[string]string
}

// Head is what HeadObject delivers - needed to take encryption and storage class
// along on a move.
type Head struct {
	Meta                 map[string]string
	ContentLength        int64
	ETag                 string
	ServerSideEncryption string
	SSEKMSKeyID          string
	BucketKeyEnabled     bool
	StorageClass         string
}

// CopyOpts are the settings a copy inherits from the original.
type CopyOpts struct {
	ServerSideEncryption string
	SSEKMSKeyID          string
	BucketKeyEnabled     bool
	StorageClass         string
}

// S3 is the slice of S3 s3mail uses. An empty byteRange fetches the whole object,
// otherwise the form is "bytes=0-65535".
type S3 interface {
	List(ctx context.Context, bucket, prefix string) ([]ObjectInfo, error)
	Get(ctx context.Context, bucket, key, byteRange string) (Object, error)
	Head(ctx context.Context, bucket, key string) (Head, error)
	Put(ctx context.Context, bucket, key string, body []byte, contentType string) error
	Delete(ctx context.Context, bucket, key string) error
	Copy(ctx context.Context, bucket, srcKey, dstKey string, o CopyOpts) error
}

// ErrNotFound is what an implementation should return for NoSuchKey.
var ErrNotFound = errors.New("objekt nicht gefunden")
