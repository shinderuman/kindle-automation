package checkworker

import (
	"context"

	"github.com/shinderuman/kindle-automation/internal/application/papertokindle"
	"github.com/shinderuman/kindle-automation/internal/domain/book"
	"github.com/shinderuman/kindle-automation/internal/storage"
)

// saleBookStore は sale.BookStore（key, asin, update）へ storage.BookFileStore（key は構築時束縛）を適合させる。
// cmd は UnprocessedKey へ束縛した store と sale.Config.UnprocessedKey に同じ値を渡すため、
// 呼び出し時の key は束縛済みと一致し、ここでは asin と update だけ転送する。
type saleBookStore struct{ inner *storage.BookFileStore }

// UpdateOneBook は key を破棄して asin と update を束縛済み store へ転送する sale.BookStore bridge。
func (s saleBookStore) UpdateOneBook(ctx context.Context, _ string, asin string, update func(book.KindleBook) book.KindleBook) (bool, error) {
	return s.inner.UpdateOneBook(ctx, asin, update)
}

// paperBooksStore は BookFileStore.Book を PaperBooksStore.PaperBook として公開する。
type paperBooksStore struct{ inner *storage.BookFileStore }

// UpdateOneBook は paper_books_asins の1件更新を Papertokindle.PaperBooksStore へ適合させる bridge。
func (s paperBooksStore) UpdateOneBook(ctx context.Context, paperASIN string, update func(book.KindleBook) book.KindleBook) (bool, error) {
	return s.inner.UpdateOneBook(ctx, paperASIN, update)
}

// Delete は paper_books_asins からの紙書籍削除を Papertokindle.PaperBooksStore へ適合させる bridge。
func (s paperBooksStore) Delete(ctx context.Context, paperASIN string) error {
	return s.inner.Delete(ctx, paperASIN)
}

// PaperBook は storage.BookFileStore.Book を PaperBooksStore.PaperBook として公開する bridge。
func (s paperBooksStore) PaperBook(ctx context.Context, paperASIN string) (book.KindleBook, bool, error) {
	return s.inner.Book(ctx, paperASIN)
}

// UpsertChanged は新刊ISBN候補による paper_books への冪等upsertと変更検知を newrelease.PaperCandidateStore へ適合させる bridge。
func (s paperBooksStore) UpsertChanged(ctx context.Context, b book.KindleBook) (bool, error) {
	return s.inner.UpsertChanged(ctx, b)
}

type paperKnownStateQuerier struct{ inner *storage.KnownStateQuerier }

// KnownState は storage.KnownState を papertokindle.KnownState へ変換する Papertokindle.KnownStateQuerier bridge。
func (q paperKnownStateQuerier) KnownState(ctx context.Context, kindleASIN, paperASIN string) (papertokindle.KnownState, error) {
	s, err := q.inner.KnownState(ctx, kindleASIN, paperASIN)
	if err != nil {
		return papertokindle.KnownState{}, err
	}
	return papertokindle.KnownState{
		NotifiedExists:    s.NotifiedExists,
		UpcomingExists:    s.UpcomingExists,
		UnprocessedExists: s.UnprocessedExists,
		PaperBookExists:   s.PaperBookExists,
	}, nil
}
