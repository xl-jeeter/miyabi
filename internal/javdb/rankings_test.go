package javdb

import (
	"net/http/httptest"
	"strconv"
	"testing"
)

func rankingsPayload(t *testing.T, body string) []byte {
	t.Helper()
	recorder := httptest.NewRecorder()
	recorder.Header().Set("Content-Type", "application/json")
	_, _ = recorder.Body.WriteString(body)
	return recorder.Body.Bytes()
}

func TestRankingsDecodesMovies(t *testing.T) {
	for _, test := range []struct {
		name    string
		payload string
		period  string
		kind    int
		wantLen int
	}{
		{
			name: "normal movies",
			payload: `{"success":1,"data":{"movies":[
				{"id":"rank-1","number":"SSIS-001","title":"Rank One","score":"4.5"}
			]}}`,
			period: "weekly", kind: 3, wantLen: 1,
		},
		{
			name: "empty movies", payload: `{"success":1,"data":{"movies":[]}}`,
			period: "invalid", kind: -1,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			transport := &fixtureTransport{responses: map[string][]byte{
				"/api/v1/rankings": rankingsPayload(t, test.payload),
			}}
			client := clientWithTransport(transport)

			movies, err := client.Rankings(t.Context(), test.period, test.kind)
			if err != nil {
				t.Fatal(err)
			}
			call := transport.calls[0]
			if call.path != "/api/v1/rankings" {
				t.Fatalf("path = %q", call.path)
			}
			wantPeriod := test.period
			wantKind := test.kind
			if test.period == "invalid" {
				wantPeriod = "daily"
				wantKind = 0
			}
			if got := call.params.Get("period"); got != wantPeriod {
				t.Errorf("period = %q, want %q", got, wantPeriod)
			}
			if got := call.params.Get("type"); got != strconv.Itoa(wantKind) {
				t.Errorf("type = %q, want %d", got, wantKind)
			}
			if len(movies) != test.wantLen {
				t.Fatalf("movies = %#v", movies)
			}
			if test.wantLen == 1 {
				movie := movies[0]
				if movie.ID != "rank-1" || movie.Code != "SSIS-001" || movie.Title != "Rank One" || movie.Rating != 4.5 {
					t.Fatalf("movie = %#v", movie)
				}
			} else if movies == nil {
				t.Fatal("empty ranking decoded as nil")
			}
		})
	}
}

func TestRankingsCacheKeyIncludesSourcePeriodAndType(t *testing.T) {
	transport := &fixtureTransport{responses: map[string][]byte{
		"/api/v1/rankings": rankingsPayload(t, `{"success":1,"data":{"movies":[]}}`),
	}}
	client := clientWithTransport(transport)

	for range 2 {
		if _, err := client.Rankings(t.Context(), "DAILY", 1); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := client.Rankings(t.Context(), "weekly", 1); err != nil {
		t.Fatal(err)
	}
	if len(transport.calls) != 2 {
		t.Fatalf("requests = %d, want 2", len(transport.calls))
	}
	for _, key := range []string{"javdb|daily|1", "javdb|weekly|1"} {
		if _, ok := client.rankingsCache[key]; !ok {
			t.Errorf("missing cache key %q in %#v", key, client.rankingsCache)
		}
	}
}
