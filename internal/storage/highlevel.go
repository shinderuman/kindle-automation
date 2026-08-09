package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/shinderuman/kindle-automation/internal/domain/book"
)

// KnownState は処理開始時の候補/対象の既知状態（SPECIFICATION.md 14.4 step1）。
// papertokindle.KnownState と同じ構造だが storage 固有型とし、cmd 層で変換する。
type KnownState struct {
	NotifiedExists    bool
	UpcomingExists    bool
	UnprocessedExists bool
	PaperBookExists   bool
}

// BookFileStore は1つの書籍JSON object（notified/upcoming/unprocessed/paper_books）へ
// 業務単位の読み書きを提供する。ObjectStore と key を1つ持つ。
// application 層の interface シグネチャ（book.KindleBook 等）と一致するメソッドを提供し、
// cmd/check-worker 層の薄い wrapper が各 application interface を満たす。
type BookFileStore struct {
	store ObjectStore
	key   string
}

// NewBookFileStore は ObjectStore と key を指定して BookFileStore を返す。
func NewBookFileStore(store ObjectStore, key string) *BookFileStore {
	return &BookFileStore{store: store, key: key}
}

// UpdateOneBook は対象 ASIN の Book を更新する。対象なし（手動削除）は applied=false。
func (s *BookFileStore) UpdateOneBook(ctx context.Context, asin string, update func(book.KindleBook) book.KindleBook) (bool, error) {
	return UpdateOneBook(ctx, s.store, s.key, asin, update)
}

// Upsert は対象 ASIN のレコードを冪等 upsert する。既存 ASIN は値を merge 更新し Extra は保持し、不存在なら追加する。重複実行で件数は増えない。
func (s *BookFileStore) Upsert(ctx context.Context, b book.KindleBook) error {
	return UpsertBookRecord(ctx, s.store, s.key, BookRecord{Book: b})
}

// Exists は対象 ASIN が存在するかを返す。保存期間適用なしの読み込みのみ。
func (s *BookFileStore) Exists(ctx context.Context, asin string) (bool, error) {
	return bookExists(ctx, s.store, s.key, asin)
}

// ApplyRetentionAndExists は発売日が now より未来でないレコードを除外（保存期間適用）し、
// 処理開始時に対象 ASIN が存在したかを返す（SPECIFICATION.md 13.6 step2-3）。
func (s *BookFileStore) ApplyRetentionAndExists(ctx context.Context, asin string, now time.Time) (bool, error) {
	var exists bool
	err := mutateBooks(ctx, s.store, s.key, defaultMergeRetries, func(records []BookRecord) ([]BookRecord, error) {
		kept := make([]BookRecord, 0, len(records))
		for _, r := range records {
			if r.Book.ASIN == asin {
				exists = true
			}
			if r.Book.ReleaseDate.After(now) {
				kept = append(kept, r)
			}
		}
		return kept, nil
	})
	return exists, err
}

// Delete は対象 ASIN を条件付きで削除する。object 自体が存在しない場合は新規保存せず no-op とする（SPECIFICATION.md 7.5 手動削除として新規保存を行わない）。冪等。
func (s *BookFileStore) Delete(ctx context.Context, asin string) error {
	if _, err := s.store.Get(ctx, s.key); err != nil {
		if errors.Is(err, ErrObjectNotFound) {
			return nil
		}
		return fmt.Errorf("get %s: %w", s.key, err)
	}
	return mutateBooks(ctx, s.store, s.key, defaultMergeRetries, func(records []BookRecord) ([]BookRecord, error) {
		kept := make([]BookRecord, 0, len(records))
		for _, r := range records {
			if r.Book.ASIN != asin {
				kept = append(kept, r)
			}
		}
		return kept, nil
	})
}

// Books は object の全 Book を並び順（S3 保存時の発売日降順・同日タイトル昇順）のまま返す。
// gist 再生成等で全件読み取るために使う。object が存在しない場合は空 slice とする。
func (s *BookFileStore) Books(ctx context.Context) ([]book.KindleBook, error) {
	obj, err := s.store.Get(ctx, s.key)
	if err != nil {
		if errors.Is(err, ErrObjectNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("get %s: %w", s.key, err)
	}
	records, err := DecodeBooks(obj.Body)
	if err != nil {
		return nil, fmt.Errorf("decode %s: %w", s.key, err)
	}
	books := make([]book.KindleBook, len(records))
	for i, r := range records {
		books[i] = r.Book
	}
	return books, nil
}

// Book は対象 ASIN の Book を返す。存在しない場合は ok=false。
func (s *BookFileStore) Book(ctx context.Context, asin string) (book.KindleBook, bool, error) {
	obj, err := s.store.Get(ctx, s.key)
	if err != nil {
		if errors.Is(err, ErrObjectNotFound) {
			return book.KindleBook{}, false, nil
		}
		return book.KindleBook{}, false, fmt.Errorf("get %s: %w", s.key, err)
	}
	records, err := DecodeBooks(obj.Body)
	if err != nil {
		return book.KindleBook{}, false, fmt.Errorf("decode %s: %w", s.key, err)
	}
	idx := findBookIndex(records, asin)
	if idx == -1 {
		return book.KindleBook{}, false, nil
	}
	return records[idx].Book, true, nil
}

// AuthorFileStore は authors.json への業務単位アクセスを提供する。
type AuthorFileStore struct {
	store ObjectStore
	key   string
}

// NewAuthorFileStore は ObjectStore と key を指定して AuthorFileStore を返す。
func NewAuthorFileStore(store ObjectStore, key string) *AuthorFileStore {
	return &AuthorFileStore{store: store, key: key}
}

// Authors は authors.json の全 Author を並び順（最新発売日降順・同日名昇順）のまま返す。
// gist 再生成で全件読み取るために使う。object が存在しない場合は空 slice とする。
func (s *AuthorFileStore) Authors(ctx context.Context) ([]book.Author, error) {
	obj, err := s.store.Get(ctx, s.key)
	if err != nil {
		if errors.Is(err, ErrObjectNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("get %s: %w", s.key, err)
	}
	records, err := DecodeAuthors(obj.Body)
	if err != nil {
		return nil, fmt.Errorf("decode %s: %w", s.key, err)
	}
	authors := make([]book.Author, len(records))
	for i, r := range records {
		authors[i] = r.Author
	}
	return authors, nil
}

// UpdateLatestRelease は候補の発売日が既存 Author の LatestReleaseDate より後なら更新する。
// 過去/将来を問わず、変更があれば true を返す。作者一覧を並べ直す（SPECIFICATION.md 13.5）。
func (s *AuthorFileStore) UpdateLatestRelease(ctx context.Context, authorName string, releaseDate time.Time, title, url string) (bool, error) {
	var changed bool
	err := mutateAuthors(ctx, s.store, s.key, defaultMergeRetries, func(records []AuthorRecord) ([]AuthorRecord, error) {
		for i, r := range records {
			if r.Author.Name == authorName && releaseDate.After(r.Author.LatestReleaseDate) {
				records[i].Author.LatestReleaseDate = releaseDate
				records[i].Author.LatestReleaseTitle = title
				records[i].Author.LatestReleaseURL = url
				changed = true
			}
		}
		return sortAuthorRecords(records), nil
	})
	return changed, err
}

// KnownStateQuerier は複数 object の存在判定から既知状態を返す（SPECIFICATION.md 14.4 step1）。
type KnownStateQuerier struct {
	store          ObjectStore
	notifiedKey    string
	upcomingKey    string
	unprocessedKey string
	paperBooksKey  string
}

// NewKnownStateQuerier は ObjectStore と各 object key を指定して KnownStateQuerier を返す。
func NewKnownStateQuerier(store ObjectStore, notifiedKey, upcomingKey, unprocessedKey, paperBooksKey string) *KnownStateQuerier {
	return &KnownStateQuerier{store: store, notifiedKey: notifiedKey, upcomingKey: upcomingKey, unprocessedKey: unprocessedKey, paperBooksKey: paperBooksKey}
}

// KnownState は処理開始時の候補/対象の既知状態を返す（SPECIFICATION.md 14.4 step1, 7.5）。
func (q *KnownStateQuerier) KnownState(ctx context.Context, kindleASIN, paperASIN string) (KnownState, error) {
	state := KnownState{}
	state.NotifiedExists = false
	if exists, err := bookExists(ctx, q.store, q.notifiedKey, kindleASIN); err != nil {
		return state, err
	} else {
		state.NotifiedExists = exists
	}
	if exists, err := bookExists(ctx, q.store, q.upcomingKey, kindleASIN); err != nil {
		return state, err
	} else {
		state.UpcomingExists = exists
	}
	if exists, err := bookExists(ctx, q.store, q.unprocessedKey, kindleASIN); err != nil {
		return state, err
	} else {
		state.UnprocessedExists = exists
	}
	if exists, err := bookExists(ctx, q.store, q.paperBooksKey, paperASIN); err != nil {
		return state, err
	} else {
		state.PaperBookExists = exists
	}
	return state, nil
}

// mutateAuthors は Get → mutate → Put(If-Match) を行い、412 で再試行する（Author 版 mutateBooks）。
func mutateAuthors(ctx context.Context, store ObjectStore, key string, maxRetry int, mutate func([]AuthorRecord) ([]AuthorRecord, error)) error {
	var lastErr error
	for attempt := 0; attempt <= maxRetry; attempt++ {
		obj, err := store.Get(ctx, key)
		if err != nil {
			if !errors.Is(err, ErrObjectNotFound) {
				return fmt.Errorf("get %s: %w", key, err)
			}
			obj = Object{Body: []byte("[]"), ETag: ""}
		}
		records, err := DecodeAuthors(obj.Body)
		if err != nil {
			return fmt.Errorf("decode %s: %w", key, err)
		}
		next, err := mutate(records)
		if err != nil {
			return err
		}
		body, err := EncodeAuthors(next)
		if err != nil {
			return fmt.Errorf("encode %s: %w", key, err)
		}
		opts := PutOptions{IfMatch: obj.ETag}
		if obj.ETag == "" {
			opts = PutOptions{IfNoneMatch: "*"}
		}
		if err := store.Put(ctx, key, body, opts); err != nil {
			if errors.Is(err, ErrPreconditionFailed) && attempt < maxRetry {
				lastErr = err
				continue
			}
			return fmt.Errorf("put %s: %w", key, err)
		}
		return nil
	}
	return fmt.Errorf("merge %s failed after %d retries: %w", key, maxRetry, lastErr)
}

// bookExists は対象 ASIN が object に存在するかを返す。
func bookExists(ctx context.Context, store ObjectStore, key, asin string) (bool, error) {
	obj, err := store.Get(ctx, key)
	if err != nil {
		if errors.Is(err, ErrObjectNotFound) {
			return false, nil
		}
		return false, fmt.Errorf("get %s: %w", key, err)
	}
	records, err := DecodeBooks(obj.Body)
	if err != nil {
		return false, fmt.Errorf("decode %s: %w", key, err)
	}
	return findBookIndex(records, asin) != -1, nil
}

// sortAuthorRecords は AuthorRecord を重複排除・並び順（最新発売日降順・同日名昇順）へ整える。
// Extra（未知 field）は保持する。
func sortAuthorRecords(records []AuthorRecord) []AuthorRecord {
	byName := make(map[string]AuthorRecord, len(records))
	authors := make([]book.Author, 0, len(records))
	for _, r := range records {
		if _, exists := byName[r.Author.Name]; !exists {
			byName[r.Author.Name] = r
			authors = append(authors, r.Author)
		}
	}
	sorted := book.SortAuthors(book.DedupAuthors(authors))
	out := make([]AuthorRecord, 0, len(sorted))
	for _, a := range sorted {
		r := byName[a.Name]
		r.Author = a
		out = append(out, r)
	}
	return out
}
