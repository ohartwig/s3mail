package awsx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"s3mail/store"
)

// S3 setzt store.S3 auf das AWS-SDK um.
type S3 struct{ c *s3.Client }

func NewS3(cfg aws.Config, endpoint string) *S3 {
	return &S3{c: s3.NewFromConfig(cfg, func(o *s3.Options) {
		if endpoint != "" { // fuer Tests und S3-Klone
			o.BaseEndpoint = aws.String(endpoint)
			o.UsePathStyle = true
		}
	})}
}

// List blaettert das Praefix vollstaendig durch - ein Postfach hat leicht mehr
// als die 1000 Objekte, die eine Seite fasst.
func (a *S3) List(ctx context.Context, bucket, prefix string) ([]store.ObjectInfo, error) {
	var out []store.ObjectInfo
	p := s3.NewListObjectsV2Paginator(a.c, &s3.ListObjectsV2Input{
		Bucket: aws.String(bucket), Prefix: aws.String(prefix)})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, translateError(err)
		}
		for _, o := range page.Contents {
			out = append(out, store.ObjectInfo{
				Key:          aws.ToString(o.Key),
				ETag:         strings.Trim(aws.ToString(o.ETag), `"`),
				Size:         aws.ToInt64(o.Size),
				LastModified: aws.ToTime(o.LastModified),
			})
		}
	}
	return out, nil
}

func (a *S3) Get(ctx context.Context, bucket, key, byteRange string) (store.Object, error) {
	in := &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)}
	if byteRange != "" {
		in.Range = aws.String(byteRange)
	}
	resp, err := a.c.GetObject(ctx, in)
	if err != nil {
		return store.Object{}, translateError(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return store.Object{}, err
	}
	return store.Object{Body: body, ETag: strings.Trim(aws.ToString(resp.ETag), `"`),
		Meta: resp.Metadata}, nil
}

func (a *S3) Head(ctx context.Context, bucket, key string) (store.Head, error) {
	resp, err := a.c.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(bucket), Key: aws.String(key)})
	if err != nil {
		return store.Head{}, translateError(err)
	}
	return store.Head{
		Meta:                 resp.Metadata,
		ContentLength:        aws.ToInt64(resp.ContentLength),
		ETag:                 strings.Trim(aws.ToString(resp.ETag), `"`),
		ServerSideEncryption: string(resp.ServerSideEncryption),
		SSEKMSKeyID:          aws.ToString(resp.SSEKMSKeyId),
		BucketKeyEnabled:     aws.ToBool(resp.BucketKeyEnabled),
		StorageClass:         string(resp.StorageClass),
	}, nil
}

func (a *S3) Put(ctx context.Context, bucket, key string, body []byte, contentType string) error {
	in := &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String(key),
		Body: strings.NewReader(string(body))}
	if contentType != "" {
		in.ContentType = aws.String(contentType)
	}
	_, err := a.c.PutObject(ctx, in)
	return translateError(err)
}

func (a *S3) Delete(ctx context.Context, bucket, key string) error {
	_, err := a.c.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(bucket), Key: aws.String(key)})
	return translateError(err)
}

// Copy nimmt Verschluesselung und Speicherklasse des Originals mit - sonst landete
// die Kopie unter dem Standardschluessel des Buckets.
func (a *S3) Copy(ctx context.Context, bucket, srcKey, dstKey string, o store.CopyOpts) error {
	in := &s3.CopyObjectInput{
		Bucket:     aws.String(bucket),
		Key:        aws.String(dstKey),
		CopySource: aws.String(url.PathEscape(bucket + "/" + srcKey)),
	}
	if o.ServerSideEncryption != "" {
		in.ServerSideEncryption = types.ServerSideEncryption(o.ServerSideEncryption)
	}
	if o.SSEKMSKeyID != "" {
		in.SSEKMSKeyId = aws.String(o.SSEKMSKeyID)
	}
	if o.BucketKeyEnabled {
		in.BucketKeyEnabled = aws.Bool(true)
	}
	if o.StorageClass != "" {
		in.StorageClass = types.StorageClass(o.StorageClass)
	}
	_, err := a.c.CopyObject(ctx, in)
	return translateError(err)
}

// translateError macht aus einem fehlenden Objekt store.ErrNichtGefunden, damit die
// Schichten darueber nicht auf AWS-Typen angewiesen sind.
func translateError(err error) error {
	if err == nil {
		return nil
	}
	var keiner *types.NoSuchKey
	var nix *types.NotFound
	if errors.As(err, &keiner) || errors.As(err, &nix) {
		return fmt.Errorf("%w: %v", store.ErrNichtGefunden, err)
	}
	var api smithy.APIError
	if errors.As(err, &api) && (api.ErrorCode() == "NoSuchKey" || api.ErrorCode() == "404" ||
		api.ErrorCode() == "NotFound") {
		return fmt.Errorf("%w: %v", store.ErrNichtGefunden, err)
	}
	return err
}
