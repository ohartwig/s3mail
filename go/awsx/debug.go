// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package awsx

import (
	"context"
	"fmt"
	"io"
	"time"

	"git.ole-hartwig.eu/development/s3mail/s3mail/go/store"
)

// What --debug writes down, and what it must never write down.
//
// The reason for a debug mode is the question "why is this slow" or "why does
// this say 403", and both are answered by which call was made and what came
// back. Neither needs a single byte of anybody's mail.
//
// So this is a decorator around the S3 interface and not the SDK's own request
// logging. ClientLogMode with LogRequest would dump bodies - which for this
// program means the mail itself, in a file, in plain text, at the moment
// somebody is debugging and least expects it. That switch is not used here and
// should not be introduced.
//
// Object keys are written: they say which mailbox and which message, which is
// exactly what makes a log useful, and they carry no content. Sizes and
// durations are written. Bodies, headers and recipient addresses are not.

// DebugTo turns on the log. Nil turns it off again.
var debugOut io.Writer

// SetDebug points the debug log at a writer, or switches it off with nil.
func SetDebug(w io.Writer) { debugOut = w }

// Debugging says whether anything is being written - the callers use it to skip
// work they would only do for the log.
func Debugging() bool { return debugOut != nil }

func debugf(format string, a ...any) {
	if debugOut == nil {
		return
	}
	fmt.Fprintf(debugOut, "%s "+format+"\n",
		append([]any{time.Now().Format("15:04:05.000")}, a...)...)
}

// WrapForDebug puts the log around any client that speaks the store interface.
// Exported for the test that matters here - the one that checks no mail ends up
// in the file - and used by NewS3Client for the real one.
func WrapForDebug(inner store.S3) store.S3 {
	if !Debugging() {
		return inner
	}
	return debugS3{inner: inner}
}

// debugS3 wraps the real client. Every method reports what it did and what came
// back, and nothing else.
type debugS3 struct{ inner store.S3 }

func (d debugS3) List(ctx context.Context, bucket, prefix string) ([]store.ObjectInfo, error) {
	start := time.Now()
	out, err := d.inner.List(ctx, bucket, prefix)
	debugf("s3 List   bucket=%s prefix=%s -> %d objects, %s%s",
		bucket, prefix, len(out), took(start), errPart(err))
	return out, err
}

func (d debugS3) Get(ctx context.Context, bucket, key, byteRange string) (store.Object, error) {
	start := time.Now()
	out, err := d.inner.Get(ctx, bucket, key, byteRange)
	debugf("s3 Get    key=%s range=%q -> %d bytes, %s%s",
		key, byteRange, len(out.Body), took(start), errPart(err))
	return out, err
}

func (d debugS3) Head(ctx context.Context, bucket, key string) (store.Head, error) {
	start := time.Now()
	out, err := d.inner.Head(ctx, bucket, key)
	debugf("s3 Head   key=%s -> %s%s", key, took(start), errPart(err))
	return out, err
}

func (d debugS3) Put(ctx context.Context, bucket, key string, body []byte, contentType string) error {
	start := time.Now()
	err := d.inner.Put(ctx, bucket, key, body, contentType)
	// The length, never the body: a Put here is a draft or a sent copy.
	debugf("s3 Put    key=%s type=%s %d bytes -> %s%s",
		key, contentType, len(body), took(start), errPart(err))
	return err
}

func (d debugS3) Delete(ctx context.Context, bucket, key string) error {
	start := time.Now()
	err := d.inner.Delete(ctx, bucket, key)
	debugf("s3 Delete key=%s -> %s%s", key, took(start), errPart(err))
	return err
}

func (d debugS3) Copy(ctx context.Context, bucket, srcKey, dstKey string, o store.CopyOpts) error {
	start := time.Now()
	err := d.inner.Copy(ctx, bucket, srcKey, dstKey, o)
	debugf("s3 Copy   %s -> %s, %s%s", srcKey, dstKey, took(start), errPart(err))
	return err
}

func took(start time.Time) string { return time.Since(start).Round(time.Millisecond).String() }

func errPart(err error) string {
	if err == nil {
		return ""
	}
	return ", error: " + err.Error()
}
