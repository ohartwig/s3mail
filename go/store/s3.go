// Package store haelt alles, was mit S3 spricht: den Index, das Verschieben und
// die Persistenz des Zustands als Op-Log. Der Zugriff laeuft ueber eine schmale
// Schnittstelle, damit die Schicht ohne AWS pruefbar bleibt.
package store

import (
	"context"
	"errors"
	"time"
)

// ObjectInfo ist ein Eintrag aus dem Listing.
type ObjectInfo struct {
	Key          string
	ETag         string
	Size         int64
	LastModified time.Time
}

// Object ist ein geholtes Objekt samt Metadaten (dort steckt der Krypto-Umschlag).
type Object struct {
	Body []byte
	ETag string
	Meta map[string]string
}

// Head ist das, was HeadObject liefert - gebraucht, um beim Verschieben
// Verschluesselung und Speicherklasse mitzunehmen.
type Head struct {
	Meta                 map[string]string
	ContentLength        int64
	ETag                 string
	ServerSideEncryption string
	SSEKMSKeyID          string
	BucketKeyEnabled     bool
	StorageClass         string
}

// CopyOpts sind die Einstellungen, die eine Kopie vom Original erbt.
type CopyOpts struct {
	ServerSideEncryption string
	SSEKMSKeyID          string
	BucketKeyEnabled     bool
	StorageClass         string
}

// S3 ist der Ausschnitt von S3, den s3mail benutzt. Ein leerer byteRange holt das
// ganze Objekt, sonst die Form "bytes=0-65535".
type S3 interface {
	List(ctx context.Context, bucket, prefix string) ([]ObjectInfo, error)
	Get(ctx context.Context, bucket, key, byteRange string) (Object, error)
	Head(ctx context.Context, bucket, key string) (Head, error)
	Put(ctx context.Context, bucket, key string, body []byte, contentType string) error
	Delete(ctx context.Context, bucket, key string) error
	Copy(ctx context.Context, bucket, srcKey, dstKey string, o CopyOpts) error
}

// ErrNichtGefunden ist das, was eine Umsetzung fuer NoSuchKey liefern soll.
var ErrNichtGefunden = errors.New("objekt nicht gefunden")
