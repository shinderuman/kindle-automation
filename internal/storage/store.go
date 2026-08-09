package storage

import (
	"context"
	"errors"
)

// Object は S3 オブジェクトの本文と ETag。
type Object struct {
	Body []byte
	ETag string
}

// ErrPreconditionFailed は If-Match / If-None-Match の前提不一致（HTTP 412）。
// S3 conditional writes の PreconditionFailed をこの sentinel へ変換する。
var ErrPreconditionFailed = errors.New("precondition failed")

// ErrObjectNotFound は Get でオブジェクトが存在しない（HTTP 404）。
var ErrObjectNotFound = errors.New("object not found")

// PutOptions は Put の条件。IfMatch は更新用、IfNoneMatch="*" は新規作成用。
type PutOptions struct {
	IfMatch     string
	IfNoneMatch string
}

// ObjectStore は S3 オブジェクトの読み書き。実装は S3 client またはテスト用 stub。
type ObjectStore interface {
	Get(ctx context.Context, key string) (Object, error)
	Put(ctx context.Context, key string, body []byte, opts PutOptions) error
}
