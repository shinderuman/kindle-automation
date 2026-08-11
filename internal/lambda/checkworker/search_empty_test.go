package checkworker

import (
	"context"
	"testing"

	"github.com/shinderuman/kindle-automation/internal/amazon"
	"github.com/shinderuman/kindle-automation/internal/application/execution"
	"github.com/shinderuman/kindle-automation/internal/application/newrelease"
	"github.com/shinderuman/kindle-automation/internal/job"
)

type nrSearchFetcher struct {
	result newrelease.SearchResult
}

func (f *nrSearchFetcher) FetchSearch(_ context.Context, _ string) (newrelease.SearchResult, error) {
	return f.result, nil
}

func TestNewReleaseSearch_EmptyClassifiedAsSearchEmptyViaDTO(t *testing.T) {
	nr := toNewReleaseSearchResult(
		amazon.SearchResult{Category: amazon.CategorySearchEmpty, HTTPStatus: 200, ResponseBytes: 42},
		"tag",
	)
	if nr.Category != newrelease.SearchEmpty {
		t.Fatalf("DTO Category = %d, want SearchEmpty(1)", nr.Category)
	}
	deps := newrelease.Dependencies{SearchFetcher: &nrSearchFetcher{result: nr}}
	j := job.Job{
		Kind:      job.KindNewReleaseSearch,
		CheckType: job.CheckNewRelease,
		Target:    job.Target{AuthorName: "海李"},
	}

	oc, err := newrelease.HandleNewReleaseSearch(context.Background(), deps, j)
	if err == nil {
		t.Fatal("want retryable error for search_empty")
	}
	if oc.Result != execution.ResultError {
		t.Errorf("result = %v, want error (retryable)", oc.Result)
	}
	if oc.ErrorType != "search_empty" {
		t.Errorf("error_type = %q, want search_empty", oc.ErrorType)
	}
	if oc.HTTPStatus != 200 || oc.ResponseBytes != 42 {
		t.Errorf("HTTPStatus=%d ResponseBytes=%d, want 200/42", oc.HTTPStatus, oc.ResponseBytes)
	}
}
