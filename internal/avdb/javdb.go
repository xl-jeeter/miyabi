package avdb

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/ppxb/miyabi/internal/domain"
	"github.com/ppxb/miyabi/internal/javdb"
)

const magnetSourceName = "avdb"

// ResolveMovieID maps a code such as MUKD-586 to its JavDB movie id through
// AVDB's search mirror. The first exact number match wins.
func (c *Client) ResolveMovieID(ctx context.Context, code string) (string, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return "", errors.New("movie code is required")
	}
	var payload envelope[searchData]
	query := url.Values{"q": {code}}
	if err := c.getJSON(ctx, "/api/v1/javdb/search", query, &payload); err != nil {
		return "", err
	}
	if payload.Code != 0 {
		return "", avdbError(payload.Message)
	}
	for _, movie := range payload.Data.Movies {
		if strings.EqualFold(strings.TrimSpace(movie.Number), code) && strings.TrimSpace(movie.ID) != "" {
			return movie.ID, nil
		}
	}
	if len(payload.Data.Movies) == 1 && strings.TrimSpace(payload.Data.Movies[0].ID) != "" {
		return payload.Data.Movies[0].ID, nil
	}
	return "", domain.E(domain.KindNotFound, fmt.Sprintf("JavDB 里查不到 %s", code), nil)
}

// MovieDetail fetches AVDB's mirror of a JavDB movie detail.
func (c *Client) MovieDetail(ctx context.Context, id string) (domain.MovieDetail, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return domain.MovieDetail{}, errors.New("AVDB movie ID is required")
	}

	var payload envelope[movieDetailData]
	path := "/api/v1/javdb/movies/" + url.PathEscape(id)
	if err := c.getJSON(ctx, path, nil, &payload); err != nil {
		return domain.MovieDetail{}, err
	}
	if payload.Code != 0 {
		return domain.MovieDetail{}, avdbError(payload.Message)
	}
	return javdb.DecodeMovieDetailObject(ctx, payload.Data.Movie)
}

// Magnets fetches AVDB's mirror of a JavDB movie's magnet list.
func (c *Client) Magnets(ctx context.Context, id string) ([]domain.Magnet, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, errors.New("AVDB movie ID is required")
	}

	var payload envelope[magnetsData]
	path := "/api/v1/javdb/movies/" + url.PathEscape(id) + "/magnets"
	if err := c.getJSON(ctx, path, nil, &payload); err != nil {
		return nil, err
	}
	if payload.Code != 0 {
		return nil, avdbError(payload.Message)
	}

	magnets := make([]domain.Magnet, len(payload.Data.Magnets))
	for index, item := range payload.Data.Magnets {
		hash, err := hex.DecodeString(item.Hash)
		if err != nil {
			return nil, fmt.Errorf("decode AVDB magnet %d hash: %w", index, err)
		}
		if len(hash) != 20 {
			return nil, fmt.Errorf("AVDB magnet %d has an invalid info hash length", index)
		}
		var tags []string
		if item.CNSub {
			tags = append(tags, domain.MagnetTagSubtitle)
		}
		if item.HD {
			tags = append(tags, domain.MagnetTagHD)
		}
		magnets[index] = domain.Magnet{
			Hash:        hex.EncodeToString(hash),
			Name:        item.Name,
			Size:        item.SizeMiB * 1024 * 1024,
			HasSubtitle: item.CNSub,
			HD:          item.HD,
			FilesCount:  item.FilesCount,
			CreatedAt:   item.CreatedAt,
			Sources:     []string{magnetSourceName},
			Tags:        tags,
		}
	}
	return magnets, nil
}

// Name identifies AVDB as a magnet source.
func (c *Client) Name() string { return magnetSourceName }

// Find retrieves AVDB magnets using the movie reference's JavDB ID.
func (c *Client) Find(ctx context.Context, ref domain.MovieRef) ([]domain.Magnet, error) {
	id := strings.TrimSpace(ref.JavDBID)
	if id == "" {
		return nil, nil
	}
	return c.Magnets(ctx, id)
}
