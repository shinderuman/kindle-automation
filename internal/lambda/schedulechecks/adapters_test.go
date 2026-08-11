package schedulechecks

import (
	"context"
	"errors"
	"testing"

	"github.com/shinderuman/kindle-automation/internal/domain/book"
	"github.com/shinderuman/kindle-automation/internal/storage"
)

func TestAsinListReader_DecodesASINs(t *testing.T) {
	store := storage.NewMemStore()
	body, _ := storage.EncodeBooks([]storage.BookRecord{
		{Book: book.KindleBook{ASIN: "B0FX3X569X"}},
		{Book: book.KindleBook{ASIN: "B0FX3X569Y"}},
	})
	store.Seed("unprocessed_asins.json", string(body))

	asins, err := asinListReader{store: store}.LoadAsins(context.Background(), "unprocessed_asins.json")
	if err != nil {
		t.Fatalf("LoadAsins: %v", err)
	}
	if len(asins) != 2 || asins[0] != "B0FX3X569X" || asins[1] != "B0FX3X569Y" {
		t.Errorf("asins = %v", asins)
	}
}

// TestAsinListReader_MissingObjectErrors は必須 object 欠落時に空配列へ fallback せず
// error を返すことを検証する（SPECIFICATION.md 9.1）。対象0件の job 投入・Gist 再生成へ進まない。
func TestAsinListReader_MissingObjectErrors(t *testing.T) {
	_, err := asinListReader{store: storage.NewMemStore()}.LoadAsins(context.Background(), "missing.json")
	if err == nil {
		t.Fatal("LoadAsins on missing object should error, got nil")
	}
	if !errors.Is(err, storage.ErrObjectNotFound) {
		t.Errorf("err = %v, want wrap of storage.ErrObjectNotFound", err)
	}
}

// TestAuthorReader_MissingObjectErrors は authors.json 欠落時に空配列へ fallback せず
// error を返すことを検証する（SPECIFICATION.md 9.1）。作者0件の周回へ進まない。
func TestAuthorReader_MissingObjectErrors(t *testing.T) {
	_, err := authorReader{store: storage.NewMemStore()}.LoadAuthorNames(context.Background(), "authors.json")
	if err == nil {
		t.Fatal("LoadAuthorNames on missing object should error, got nil")
	}
	if !errors.Is(err, storage.ErrObjectNotFound) {
		t.Errorf("err = %v, want wrap of storage.ErrObjectNotFound", err)
	}
}

func TestAuthorReader_DecodesNames(t *testing.T) {
	store := storage.NewMemStore()
	body, _ := storage.EncodeAuthors([]storage.AuthorRecord{
		{Author: book.Author{Name: "海李"}},
		{Author: book.Author{Name: "上原誠"}},
	})
	store.Seed("authors.json", string(body))

	names, err := authorReader{store: store}.LoadAuthorNames(context.Background(), "authors.json")
	if err != nil {
		t.Fatalf("LoadAuthorNames: %v", err)
	}
	if len(names) != 2 || names[0] != "海李" || names[1] != "上原誠" {
		t.Errorf("names = %v", names)
	}
}
