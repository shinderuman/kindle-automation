package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/shinderuman/kindle-automation/internal/domain/book"
)

// SPECIFICATION.md 14.4 step1 の処理開始時既知状態。papertokindle.KnownState と同じ構造だが storage 固有型とし、cmd 層で変換する。
type KnownState struct {
	NotifiedExists    bool
	UpcomingExists    bool
	UnprocessedExists bool
	PaperBookExists   bool
}

// 1つの書籍JSON object（notified/upcoming/unprocessed/paper_books）へ業務単位の読み書きを提供する。
// application 層の interface シグネチャ（book.KindleBook 等）と一致するメソッドを提供し、
// cmd/check-worker 層の薄い wrapper が各 application interface を満たす。
type BookFileStore struct {
	store ObjectStore
	key   string
}

func NewBookFileStore(store ObjectStore, key string) *BookFileStore {
	return &BookFileStore{store: store, key: key}
}

// 対象 ASIN が無ければ手動削除扱いで applied=false を返す（再追加しない）。
func (s *BookFileStore) UpdateOneBook(ctx context.Context, asin string, update func(book.KindleBook) book.KindleBook) (bool, error) {
	return UpdateOneBook(ctx, s.store, s.key, asin, update)
}

// 冪等 upsert: 既存 ASIN は値を merge 更新し Extra は保持し、不存在なら追加する。重複実行で件数は増えない。
func (s *BookFileStore) Upsert(ctx context.Context, b book.KindleBook) error {
	return UpsertBookRecord(ctx, s.store, s.key, BookRecord{Book: b})
}

// 保存期間適用なしの読み込みのみ。
func (s *BookFileStore) Exists(ctx context.Context, asin string) (bool, error) {
	return bookExists(ctx, s.store, s.key, asin)
}

// SPECIFICATION.md 13.6 step2-3: 発売日が now より未来でないレコードを除外（保存期間適用）し、
// 処理開始時に対象 ASIN が存在したかを返す。
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

// SPECIFICATION.md 9.1/7.5: 対象 object は存在必須のため object 自体の欠落は error（空 fallback や新規保存はしない）。
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

// SPECIFICATION.md 9.1: 対象 object は存在必須のため、ErrObjectNotFound を含む Get error をそのまま返し、
// 空配列へ fallback して Gist を空上書きしない。
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

// SPECIFICATION.md 9.1: 対象 object は存在必須のため、object 自体の欠落は ok=false ではなく error とし、
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

type AuthorFileStore struct {
	store ObjectStore
	key   string
}

func NewAuthorFileStore(store ObjectStore, key string) *AuthorFileStore {
	return &AuthorFileStore{store: store, key: key}
}

// SPECIFICATION.md 9.1: authors.json は存在必須 object のため、ErrObjectNotFound を含む Get error をそのまま返し、
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

// SPECIFICATION.md 13.5: 候補の発売日が既存 Author の LatestReleaseDate より後なら更新し、作者一覧を並べ直す。
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

type KnownStateQuerier struct {
	store          ObjectStore
	notifiedKey    string
	upcomingKey    string
	unprocessedKey string
	paperBooksKey  string
}

func NewKnownStateQuerier(store ObjectStore, notifiedKey, upcomingKey, unprocessedKey, paperBooksKey string) *KnownStateQuerier {
	return &KnownStateQuerier{store: store, notifiedKey: notifiedKey, upcomingKey: upcomingKey, unprocessedKey: unprocessedKey, paperBooksKey: paperBooksKey}
}

// SPECIFICATION.md 14.4 step1, 7.5: 処理開始時の候補/対象の既知状態を返す。
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

// Author 版 mutateBooks: Get → mutate → Put(If-Match) を行い、412 で再試行する。
// authors.json は SPECIFICATION.md 9.1 の存在必須 object のため、ErrObjectNotFound を含む Get error を
// 呼出側へ返し、空配列への fallback や If-None-Match: * による暗黙の新規作成は行わない。
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

// 重複排除・並び順（最新発売日降順・同日名昇順）へ整える。Extra（未知 field）は保持する。
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
