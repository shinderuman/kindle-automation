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

// UpdateOneBook は対象 ASIN のレコードを更新する。手動削除時は applied=false。
func (s saleBookStore) UpdateOneBook(ctx context.Context, _ string, asin string, update func(book.KindleBook) book.KindleBook) (bool, error) {
	return s.inner.UpdateOneBook(ctx, asin, update)
}

// paperBooksStore は storage.BookFileStore を papertokindle.PaperBooksStore へ適合させる。
// BookFileStore.Book を PaperBook として公開する。
type paperBooksStore struct{ inner *storage.BookFileStore }

func (s paperBooksStore) UpdateOneBook(ctx context.Context, paperASIN string, update func(book.KindleBook) book.KindleBook) (bool, error) {
	return s.inner.UpdateOneBook(ctx, paperASIN, update)
}

func (s paperBooksStore) Delete(ctx context.Context, paperASIN string) error {
	return s.inner.Delete(ctx, paperASIN)
}

func (s paperBooksStore) PaperBook(ctx context.Context, paperASIN string) (book.KindleBook, bool, error) {
	return s.inner.Book(ctx, paperASIN)
}

// paperKnownStateQuerier は storage.KnownState を papertokindle.KnownState へ変換する薄い adapter。
type paperKnownStateQuerier struct{ inner *storage.KnownStateQuerier }

// KnownState は処理開始時の候補/対象の既知状態を返す。
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
