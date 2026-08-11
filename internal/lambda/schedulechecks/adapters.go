package schedulechecks

import (
	"context"
	"fmt"

	"github.com/shinderuman/kindle-automation/internal/storage"
)

// mergeMaxRetries は Upcoming→Unprocessed 条件付き merge の前提不一致再試行回数（SPECIFICATION.md 9.5 は最大3回）。
const mergeMaxRetries = 3

// asinListReader は書籍JSON object から ASIN 一覧を読み取る dispatch.AsinListReader。
type asinListReader struct{ store storage.ObjectStore }

// LoadAsins は対象 object の全 ASIN を返す。対象 object（unprocessed_asins・paper_books_asins）は
// SPECIFICATION.md 9.1 の存在必須 object のため、ErrObjectNotFound を含む Get error をそのまま返し、
// 空配列へ fallback して対象0件の job 投入や Gist 再生成へ進まない。
func (r asinListReader) LoadAsins(ctx context.Context, key string) ([]string, error) {
	obj, err := r.store.Get(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("get %s: %w", key, err)
	}
	records, err := storage.DecodeBooks(obj.Body)
	if err != nil {
		return nil, fmt.Errorf("decode %s: %w", key, err)
	}
	asins := make([]string, 0, len(records))
	for _, rec := range records {
		asins = append(asins, rec.Book.ASIN)
	}
	return asins, nil
}

// authorReader は authors.json から作者名一覧を読み取る dispatch.AuthorReader。
type authorReader struct{ store storage.ObjectStore }

// LoadAuthorNames は authors.json の全 Name を返す。authors.json は SPECIFICATION.md 9.1 の存在必須 objectのため、
// ErrObjectNotFound を含む Get error をそのまま返し、空配列へ fallback して作者0件の周回へ進まない。
func (r authorReader) LoadAuthorNames(ctx context.Context, key string) ([]string, error) {
	obj, err := r.store.Get(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("get %s: %w", key, err)
	}
	records, err := storage.DecodeAuthors(obj.Body)
	if err != nil {
		return nil, fmt.Errorf("decode %s: %w", key, err)
	}
	names := make([]string, 0, len(records))
	for _, rec := range records {
		names = append(names, rec.Author.Name)
	}
	return names, nil
}

// upcomingMerger はセール周期開始時の Upcoming→Unprocessed merge を行う dispatch.UpcomingMerger。
type upcomingMerger struct {
	store          storage.ObjectStore
	unprocessedKey string
	upcomingKey    string
}

// MergeUpcoming は Upcoming を Unprocessed へ条件付き merge する（SPECIFICATION.md 10）。
func (m upcomingMerger) MergeUpcoming(ctx context.Context) (int, error) {
	return storage.MergeUpcoming(ctx, m.store, m.unprocessedKey, m.upcomingKey, mergeMaxRetries)
}
