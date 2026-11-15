package javdb

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestDecodeMovieDetailObject(t *testing.T) {
	var envelope struct {
		Data struct {
			Movie json.RawMessage `json:"movie"`
		} `json:"data"`
	}
	if err := json.Unmarshal(fixtureFile(t, "movie.json"), &envelope); err != nil {
		t.Fatal(err)
	}

	movie, err := DecodeMovieDetailObject(t.Context(), envelope.Data.Movie)
	if err != nil {
		t.Fatal(err)
	}
	if movie.ID != "movie-exact" || movie.Code != "ABP-123" || movie.Zone != zoneFromCode(0) ||
		len(movie.ActorMovies) != 1 || len(movie.RelatedMovies) != 1 {
		t.Fatalf("decoded movie = %#v", movie)
	}

	if _, err := DecodeMovieDetailObject(t.Context(), json.RawMessage(`{"id":"missing-code"}`)); err == nil {
		t.Fatal("accepted an invalid movie object")
	}
	if _, err := DecodeMovieDetailObject(t.Context(), json.RawMessage(`{`)); err == nil {
		t.Fatal("accepted malformed JSON")
	}
}

func TestIsAuthFailureIncludesAPIAndTextErrors(t *testing.T) {
	authErrors := []error{
		&HTTPError{StatusCode: 401},
		&APIError{Action: "JWTVerificationError", Message: "invalid token"},
		errors.New("GET failed: JWTVerificationError"),
		errors.New("請登錄帳號後繼續"),
	}
	for _, err := range authErrors {
		if !IsAuthFailure(err) {
			t.Errorf("IsAuthFailure(%v) = false", err)
		}
	}
	if IsAuthFailure(errors.New("temporary outage")) {
		t.Fatal("classified a transient failure as an auth failure")
	}
}
