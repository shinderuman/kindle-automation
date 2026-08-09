package schedulechecks

import (
	"context"
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

func TestAsinListReader_MissingObjectIsEmpty(t *testing.T) {
	asins, err := asinListReader{store: storage.NewMemStore()}.LoadAsins(context.Background(), "missing.json")
	if err != nil {
		t.Fatalf("LoadAsins missing object should be empty, got err: %v", err)
	}
	if len(asins) != 0 {
		t.Errorf("asins = %v, want empty", asins)
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
