package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	awstypes "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

// S3Store は AWS SDK for Go v2 による ObjectStore 実装。
// ETag 付き読込と If-Match / If-None-Match 条件付き PutObject を行う（SPECIFICATION.md 9.5）。
// 実AWS接続を除く単体テストは MemStore で検証し、この実装はデプロイ時に確認する。
type S3Store struct {
	client *s3.Client
	bucket string
}

// NewS3Store は S3Store を返す。client は外部で構築した S3 client を注入する。
func NewS3Store(client *s3.Client, bucket string) *S3Store {
	return &S3Store{client: client, bucket: bucket}
}

// Get はオブジェクト本文と ETag を返す。存在しない場合は ErrObjectNotFound。
func (s *S3Store) Get(ctx context.Context, key string) (Object, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		var nfe *awstypes.NotFound
		var nsk *awstypes.NoSuchKey
		if errors.As(err, &nfe) || errors.As(err, &nsk) {
			return Object{}, ErrObjectNotFound
		}
		return Object{}, fmt.Errorf("s3 get %s: %w", key, err)
	}
	defer out.Body.Close()
	body, err := io.ReadAll(out.Body)
	if err != nil {
		return Object{}, fmt.Errorf("s3 read body %s: %w", key, err)
	}
	etag := ""
	if out.ETag != nil {
		etag = *out.ETag
	}
	return Object{Body: body, ETag: etag}, nil
}

// Put は本文を書き込む。IfMatch は ETag 一致更新、IfNoneMatch="*" は新規作成のみ。
// 前提不一致（HTTP 412）は ErrPreconditionFailed へ変換する。
func (s *S3Store) Put(ctx context.Context, key string, body []byte, opts PutOptions) error {
	input := &s3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
		Body:   bytes.NewReader(body),
	}
	if opts.IfMatch != "" {
		input.IfMatch = aws.String(opts.IfMatch)
	}
	if opts.IfNoneMatch == "*" {
		input.IfNoneMatch = aws.String("*")
	}
	if _, err := s.client.PutObject(ctx, input); err != nil {
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) && apiErr.ErrorCode() == "PreconditionFailed" {
			return ErrPreconditionFailed
		}
		return fmt.Errorf("s3 put %s: %w", key, err)
	}
	return nil
}
