package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/ppxb/miyabi/internal/domain"
	"github.com/ppxb/miyabi/internal/javdb"
)

type rankingsStub struct {
	enabled bool
	movies  []domain.Movie
	err     error
	calls   int
}

func (stub *rankingsStub) Enabled() bool { return stub.enabled }

func (stub *rankingsStub) Rankings(context.Context, string, int) ([]domain.Movie, error) {
	stub.calls++
	return stub.movies, stub.err
}

func TestRankingsSourcePrefersJavDB(t *testing.T) {
	primary := &rankingsStub{movies: []domain.Movie{{ID: "javdb-rank"}}}
	fallback := &rankingsStub{enabled: true, movies: []domain.Movie{{ID: "avdb-rank"}}}
	source := NewRankingsSource(primary, fallback, slog.New(slog.NewTextHandler(io.Discard, nil)))

	movies, err := source.Rankings(t.Context(), "daily", 0)
	if err != nil || len(movies) != 1 || movies[0].ID != "javdb-rank" {
		t.Fatalf("rankings = %#v, %v", movies, err)
	}
	if primary.calls != 1 || fallback.calls != 0 {
		t.Fatalf("calls = primary %d, fallback %d", primary.calls, fallback.calls)
	}
}

func TestRankingsSourceFallsBackAfterJavDBAuthFailure(t *testing.T) {
	authErr := &javdb.APIError{Action: "JWTVerificationError", Message: "login required"}
	if !javdb.IsAuthFailure(authErr) {
		t.Fatal("fixture is not classified as an auth failure")
	}
	primary := &rankingsStub{err: authErr}
	fallback := &rankingsStub{enabled: true, movies: []domain.Movie{{ID: "avdb-rank"}}}
	source := NewRankingsSource(primary, fallback, slog.New(slog.NewTextHandler(io.Discard, nil)))

	movies, err := source.Rankings(t.Context(), "daily", 0)
	if err != nil || len(movies) != 1 || movies[0].ID != "avdb-rank" {
		t.Fatalf("rankings = %#v, %v", movies, err)
	}
	if primary.calls != 1 || fallback.calls != 1 {
		t.Fatalf("calls = primary %d, fallback %d", primary.calls, fallback.calls)
	}
}

func TestRankingsSourceReportsBothFailures(t *testing.T) {
	primary := &rankingsStub{err: errors.New("javdb unavailable")}
	fallback := &rankingsStub{enabled: true, err: errors.New("avdb unavailable")}
	source := NewRankingsSource(primary, fallback, slog.New(slog.NewTextHandler(io.Discard, nil)))

	_, err := source.Rankings(t.Context(), "daily", 0)
	if err == nil {
		t.Fatal("expected both ranking sources to fail")
	}
	for _, reason := range []string{"javdb unavailable", "avdb unavailable"} {
		if !strings.Contains(err.Error(), reason) {
			t.Errorf("error %q does not contain %q", err, reason)
		}
	}
}
