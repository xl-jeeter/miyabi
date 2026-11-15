package avdb

import (
	"net/http"
	"testing"
)

func TestResolveMovieIDUsesSearchMirror(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/api/v1/javdb/search" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("q"); got != "MUKD-586" {
			t.Fatalf("q = %q", got)
		}
		return jsonResponse(r, `{"code":0,"message":"ok","data":{"movies":[
			{"id":"wrongID","number":"MUKD-585"},
			{"id":"6dnmRK","number":"mukd-586"}
		]}}`), nil
	})

	client := New(Options{BaseURL: "https://avdb.test", APIKey: "test-key"})
	client.http.SetTransport(transport)
	id, err := client.ResolveMovieID(t.Context(), "MUKD-586")
	if err != nil {
		t.Fatal(err)
	}
	if id != "6dnmRK" {
		t.Fatalf("id = %q, want 6dnmRK", id)
	}
}

func TestResolveMovieIDFallsBackToSingleResult(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(r, `{"code":0,"data":{"movies":[{"id":"abc123","number":"FC2-4996423"}]}}`), nil
	})
	client := New(Options{BaseURL: "https://avdb.test", APIKey: "test-key"})
	client.http.SetTransport(transport)
	id, err := client.ResolveMovieID(t.Context(), "FC2-4996423")
	if err != nil || id != "abc123" {
		t.Fatalf("id=%q err=%v", id, err)
	}
}

func TestResolveMovieIDReportsMissingCode(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(r, `{"code":0,"data":{"movies":[]}}`), nil
	})
	client := New(Options{BaseURL: "https://avdb.test", APIKey: "test-key"})
	client.http.SetTransport(transport)
	if _, err := client.ResolveMovieID(t.Context(), "NOPE-001"); err == nil {
		t.Fatal("expected an error for a code with no match")
	}
}
