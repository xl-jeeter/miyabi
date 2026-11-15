package catalogue

import (
	"context"
	"errors"
	"testing"

	"github.com/ppxb/miyabi/internal/domain"
	"github.com/ppxb/miyabi/internal/javdb"
)

type failingDetailProvider struct {
	stubProviderWithMagnets
	err   error
	calls int
}

func (p *failingDetailProvider) MovieDetail(context.Context, string) (domain.MovieDetail, error) {
	p.calls++
	return domain.MovieDetail{}, p.err
}

type catalogueAVDBStub struct {
	enabled     bool
	detail      domain.MovieDetail
	magnets     []domain.Magnet
	detailCalls int
	findCalls   int
	resolvedID  string
}

func (s *catalogueAVDBStub) Enabled() bool { return s.enabled }
func (s *catalogueAVDBStub) Name() string  { return "avdb" }
func (s *catalogueAVDBStub) MovieDetail(context.Context, string) (domain.MovieDetail, error) {
	s.detailCalls++
	return s.detail, nil
}
func (s *catalogueAVDBStub) Find(context.Context, domain.MovieRef) ([]domain.Magnet, error) {
	s.findCalls++
	return s.magnets, nil
}
func (s *catalogueAVDBStub) ResolveMovieID(_ context.Context, code string) (string, error) {
	if s.resolvedID == "" {
		return "", errors.New("no resolution configured for " + code)
	}
	return s.resolvedID, nil
}

func TestMovieDetailFallsBackToAVDBOnlyForAuthFailures(t *testing.T) {
	tests := []struct {
		name         string
		err          error
		wantFallback bool
	}{
		{name: "typed auth error", err: &javdb.APIError{Action: "JWTVerificationError"}, wantFallback: true},
		{name: "auth text", err: errors.New("請登錄帳號"), wantFallback: true},
		{name: "transient error", err: errors.New("temporary outage")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			primary := &failingDetailProvider{err: test.err}
			fallback := &catalogueAVDBStub{
				enabled: true,
				detail:  domain.MovieDetail{Movie: domain.Movie{ID: "movie-1", Code: "SSIS-001"}},
			}
			service := newService(nil, primary, nil, &stubLocalState{}, persistedRoute{}, fallback)
			defer service.Close()

			detail, err := service.MovieDetail(t.Context(), "movie-1")
			if test.wantFallback {
				if err != nil || detail.ID != "movie-1" || fallback.detailCalls != 1 {
					t.Fatalf("fallback detail = %#v, %v, calls = %d", detail, err, fallback.detailCalls)
				}
				if _, err := service.MovieDetail(t.Context(), "movie-1"); err != nil || fallback.detailCalls != 1 {
					t.Fatalf("fallback result was not cached: %v, calls = %d", err, fallback.detailCalls)
				}
			} else if err == nil || fallback.detailCalls != 0 {
				t.Fatalf("non-auth failure used fallback: detail = %#v, %v, calls = %d", detail, err, fallback.detailCalls)
			}
		})
	}
}

type failingMagnetProvider struct {
	stubProviderWithMagnets
}

func (p *failingMagnetProvider) Find(context.Context, domain.MovieRef) ([]domain.Magnet, error) {
	return nil, errors.New("primary unavailable")
}

func TestAVDBMagnetsAreAppendedOnlyWhenConfigured(t *testing.T) {
	const hash = "abcdef0123456789abcdef0123456789abcdef01"
	avdbMagnets := []domain.Magnet{{Hash: hash, Name: "from avdb", Sources: []string{"avdb"}}}

	enabled := &catalogueAVDBStub{enabled: true, magnets: avdbMagnets}
	service := newService(nil, &failingMagnetProvider{}, nil, &stubLocalState{}, persistedRoute{}, enabled)
	defer service.Close()
	magnets, err := service.Magnets(t.Context(), "movie-1")
	if err != nil || len(magnets) != 1 || magnets[0].Hash != hash || enabled.findCalls != 1 {
		t.Fatalf("enabled AVDB magnets = %#v, %v, calls = %d", magnets, err, enabled.findCalls)
	}

	disabled := &catalogueAVDBStub{magnets: avdbMagnets}
	service = newService(nil, &failingMagnetProvider{}, nil, &stubLocalState{}, persistedRoute{}, disabled)
	defer service.Close()
	if _, err := service.Magnets(t.Context(), "movie-1"); err == nil || disabled.findCalls != 0 {
		t.Fatalf("disabled AVDB participated: %v, calls = %d", err, disabled.findCalls)
	}
}
