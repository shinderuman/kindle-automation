package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/shinderuman/kindle-automation/internal/domain/book"
)

// KnownState は SPECIFICATION.md 14.4 step1 の処理開始時既知状態。papertokindle.KnownState と同じ構造だが storage 固有型とし、cmd 層で変換する。
type KnownState struct {
	NotifiedExists    bool
	UpcomingExists    bool
	UnprocessedExists bool
	PaperBookExists   bool
}

// BookFileStore は1つの書籍JSON object（notified/upcoming/unprocessed/paper_books）への読み書きを提供し、
// cmd/check-worker 層の薄い wrapper が各 application interface を満たす。
type BookFileStore struct {
	store ObjectStore
	key   string
}

// NewBookFileStore は ObjectStore と key から1つの書籍JSON object への読み書き wrapper を組み立てる。
func NewBookFileStore(store ObjectStore, key string) *BookFileStore {
	return &BookFileStore{store: store, key: key}
}

// UpdateOneBook は対象 ASIN のレコードを更新する。対象 ASIN が無ければ手動削除扱いで applied=false を返す（再追加しない、SPECIFICATION.md 7.2 target_removed）。
func (s *BookFileStore) UpdateOneBook(ctx context.Context, asin string, update func(book.KindleBook) book.KindleBook) (bool, error) {
	return UpdateOneBook(ctx, s.store, s.key, asin, update)
}

// Upsert は ASIN 単位の冪等 upsert。既存 ASIN は値を merge 更新し Extra は保持し、不存在なら追加する（重複実行で件数は増えない、SPECIFICATION.md 7.4）。
func (s *BookFileStore) Upsert(ctx context.Context, b book.KindleBook) error {
	return UpsertBookRecord(ctx, s.store, s.key, BookRecord{Book: b})
}

// Exists は対象 ASIN が object 内に存在するかを返す（SPECIFICATION.md 9.1 の存在必須 object 前提）。
func (s *BookFileStore) Exists(ctx context.Context, asin string) (bool, error) {
	return bookExists(ctx, s.store, s.key, asin)
}

// ApplyRetentionAndExists は SPECIFICATION.md 13.6 step2-3 の保存期間適用を行う。
// 発売日が now より未来でないレコードを除外し、処理開始時に対象 ASIN が存在したかを返す。
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

// Delete は対象 ASIN のレコードを除外保存する。対象 object は存在必須のため object 自体の欠落は error（空 fallback や新規保存はしない、SPECIFICATION.md 9.1/7.5）。
// object 内に ASIN がなければ（手動削除済み）何も削除せず成功する（冪等）。
func (s *BookFileStore) Delete(ctx context.Context, asin string) error {
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

// Books は書籍JSON object を読み込み KindleBook 配列へ復号して返す（SPECIFICATION.md 9.1）。
// 対象 object は存在必須のため、ErrObjectNotFound を含む Get error をそのまま返し、空配列へ fallback して Gist を空上書きしない。
func (s *BookFileStore) Books(ctx context.Context) ([]book.KindleBook, error) {
	obj, err := s.store.Get(ctx, s.key)
	if err != nil {
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

// Book は対象 ASIN の KindleBook を取得する（SPECIFICATION.md 9.1）。
// 対象 object は存在必須のため、object 自体の欠落は ok=false ではなく error とし、
// 「ASIN が存在しない」と同一視しない（object 内に ASIN が無ければ ok=false）。
func (s *BookFileStore) Book(ctx context.Context, asin string) (book.KindleBook, bool, error) {
	obj, err := s.store.Get(ctx, s.key)
	if err != nil {
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

// AuthorFileStore は authors.json への読み書きを提供し、application 層 interface を満たす。
type AuthorFileStore struct {
	store ObjectStore
	key   string
}

// NewAuthorFileStore は ObjectStore と key から authors.json の読み書き wrapper を組み立てる。
func NewAuthorFileStore(store ObjectStore, key string) *AuthorFileStore {
	return &AuthorFileStore{store: store, key: key}
}

// Authors は authors.json を読み込み Author 配列へ復号して返す（SPECIFICATION.md 9.1）。
// authors.json は存在必須 object のため、ErrObjectNotFound を含む Get error をそのまま返し、
// 空配列へ fallback して作者0件の正本を再生成しない。
func (s *AuthorFileStore) Authors(ctx context.Context) ([]book.Author, error) {
	obj, err := s.store.Get(ctx, s.key)
	if err != nil {
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

// UpdateLatestRelease は候補の発売日が既存 Author の LatestReleaseDate より後なら更新し、作者一覧を並べ直す（SPECIFICATION.md 13.5）。
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

// KnownStateQuerier は処理開始時の既知状態を引くための querier。
type KnownStateQuerier struct {
	store          ObjectStore
	notifiedKey    string
	upcomingKey    string
	unprocessedKey string
	paperBooksKey  string
}

// NewKnownStateQuerier は各書籍JSON object の key を指定して KnownStateQuerier を組み立てる。
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

// mutateBooks と同様に Get→mutate→Put(If-Match) で412再試行し、存在必須 object なので Get error を
// fallback/新規作成せずそのまま返す（SPECIFICATION.md 9.1/9.5）。
func mutateAuthors(ctx context.Context, store ObjectStore, key string, maxRetry int, mutate func([]AuthorRecord) ([]AuthorRecord, error)) error {
	var lastErr error
	for attempt := 0; attempt <= maxRetry; attempt++ {
		obj, err := store.Get(ctx, key)
		if err != nil {
			return fmt.Errorf("get %s: %w", key, err)
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
		if err := store.Put(ctx, key, body, PutOptions{IfMatch: obj.ETag}); err != nil {
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

// SPECIFICATION.md 9.1: 対象 object は存在必須のため、object 自体の欠落は exists=false ではなく error とし、
// 「ASIN が存在しない」と同一視しない。
func bookExists(ctx context.Context, store ObjectStore, key, asin string) (bool, error) {
	obj, err := store.Get(ctx, key)
	if err != nil {
		return false, fmt.Errorf("get %s: %w", key, err)
	}
	records, err := DecodeBooks(obj.Body)
	if err != nil {
		return false, fmt.Errorf("decode %s: %w", key, err)
	}
	return findBookIndex(records, asin) != -1, nil
}

// AuthorRecord 単位で dedup/sort し Extra を保持する（domain book 側へ渡すと Extra を失うため）。
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
