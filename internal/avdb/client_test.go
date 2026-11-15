package avdb

import (
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ppxb/miyabi/internal/domain"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func jsonResponse(request *http.Request, body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    request,
	}
}

func TestJavDBMirrorEndpoints(t *testing.T) {
	var requests atomic.Int32
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests.Add(1)
		if r.Header.Get("X-API-Key") != "test-key" {
			t.Errorf("X-API-Key = %q", r.Header.Get("X-API-Key"))
		}
		switch r.URL.Path {
		case "/api/v1/javdb/movies/movie-1":
			return jsonResponse(r, `{"code":0,"message":"ok","data":{"movie":{"id":"movie-1","number":"SSIS-001","type":0,"title":"Detail","score":4.5}}}`), nil
		case "/api/v1/javdb/movies/movie-1/magnets":
			return jsonResponse(r, `{"code":0,"data":{"magnets":[{"name":"Subtitle","hash":"ABCDEF0123456789ABCDEF0123456789ABCDEF01","size":3902,"cnsub":true,"hd":true,"files_count":2,"created_at":"2026-11-10"}]}}`), nil
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
		}
	})

	client := New(Options{BaseURL: "https://avdb.test", APIKey: "test-key"})
	client.http.SetTransport(transport)
	movie, err := client.MovieDetail(t.Context(), "movie-1")
	if err != nil {
		t.Fatal(err)
	}
	if movie.ID != "movie-1" || movie.Code != "SSIS-001" || movie.Zone != domain.ZoneCensored {
		t.Fatalf("movie = %#v", movie)
	}

	magnets, err := client.Magnets(t.Context(), "movie-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(magnets) != 1 {
		t.Fatalf("magnets = %#v", magnets)
	}
	magnet := magnets[0]
	if magnet.Hash != "abcdef0123456789abcdef0123456789abcdef01" ||
		magnet.Size != 3902*1024*1024 || !magnet.HasSubtitle || !magnet.HD ||
		magnet.FilesCount != 2 || magnet.CreatedAt != "2026-11-10" {
		t.Fatalf("magnet = %#v", magnet)
	}
	if len(magnet.Sources) != 1 || magnet.Sources[0] != "avdb" {
		t.Fatalf("sources = %#v", magnet.Sources)
	}

	found, err := client.Find(t.Context(), domain.MovieRef{JavDBID: "movie-1"})
	if err != nil || len(found) != 1 {
		t.Fatalf("Find = %#v, %v", found, err)
	}
	if client.Name() != "avdb" {
		t.Fatalf("Name = %q", client.Name())
	}
	if skipped, err := client.Find(t.Context(), domain.MovieRef{}); err != nil || skipped != nil {
		t.Fatalf("empty Find = %#v, %v", skipped, err)
	}
	if got := requests.Load(); got != 3 {
		t.Fatalf("requests = %d, want 3", got)
	}
}

func TestJavDBMirrorErrors(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/api/v1/javdb/movies/rejected" {
			return jsonResponse(r, `{"code":7,"message":"rejected"}`), nil
		}
		return jsonResponse(r, `{"code":0,"data":{"magnets":[{"hash":"invalid"}]}}`), nil
	})

	client := New(Options{BaseURL: "https://avdb.test", APIKey: "test-key"})
	client.http.SetTransport(transport)
	if _, err := client.MovieDetail(t.Context(), "rejected"); err == nil {
		t.Fatal("accepted an AVDB error envelope")
	}
	if _, err := client.Magnets(t.Context(), "movie-1"); err == nil {
		t.Fatal("accepted a malformed magnet hash")
	}
	if _, err := client.MovieDetail(t.Context(), " "); err == nil {
		t.Fatal("accepted an empty movie ID")
	}
}
