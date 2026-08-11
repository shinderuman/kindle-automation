package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sync"
)

// MemStore はテスト用のメモリ ObjectStore。ETag は本文の SHA-256 を使う。
// 実 AWS へ接続せず、If-Match / If-None-Match の前提不一致で ErrPreconditionFailed を返す。
type MemStore struct {
	mu      sync.Mutex
	objects map[string]string
}

// NewMemStore は空のテスト用 ObjectStore を組み立てる。
func NewMemStore() *MemStore {
	return &MemStore{objects: make(map[string]string)}
}

// Get は object が存在しないと ErrObjectNotFound を返す。
func (s *MemStore) Get(_ context.Context, key string) (Object, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	body, ok := s.objects[key]
	if !ok {
		return Object{}, ErrObjectNotFound
	}
	return Object{Body: []byte(body), ETag: memETag(body)}, nil
}

// Put は If-Match / If-None-Match の前提不一致で ErrPreconditionFailed を返す。
func (s *MemStore) Put(_ context.Context, key string, body []byte, opts PutOptions) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, exists := s.objects[key]
	if opts.IfNoneMatch == "*" && exists {
		return ErrPreconditionFailed
	}
	if opts.IfMatch != "" {
		if !exists || memETag(existing) != opts.IfMatch {
			return ErrPreconditionFailed
		}
	}
	s.objects[key] = string(body)
	return nil
}

// Seed はテスト専用の初期データ設定（本番コードからは使わない）。
func (s *MemStore) Seed(key string, body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[key] = body
}

func memETag(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}
