package javdb

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ppxb/miyabi/internal/domain"
)

const rankingsCacheTTL = 5 * time.Minute

type cachedRankings struct {
	movies  []domain.Movie
	expires time.Time
}

// Rankings returns one JavDB ranking list. Invalid periods and kinds are
// normalized to the App API defaults.
func (c *Client) Rankings(ctx context.Context, period string, kind int) ([]domain.Movie, error) {
	normalized := normalizeRankingPeriod(period)
	kind = normalizeRankingKind(kind)
	cacheKey := fmt.Sprintf("javdb|%s|%d", normalized, kind)
	if movies, hit := c.rankingsFromCache(cacheKey); hit {
		return movies, nil
	}

	params := url.Values{
		"type":   {strconv.Itoa(kind)},
		"period": {string(normalized)},
	}
	var data wireMoviesData
	if err := c.getJSON(ctx, "/api/v1/rankings", params, &data); err != nil {
		return nil, err
	}
	movies, err := moviesFromWire(ctx, data.Movies)
	if err != nil {
		return nil, err
	}
	c.storeRankings(cacheKey, movies)
	return movies, nil
}

type rankingPeriod string

const (
	rankingPeriodDaily   rankingPeriod = "daily"
	rankingPeriodWeekly  rankingPeriod = "weekly"
	rankingPeriodMonthly rankingPeriod = "monthly"
)

func normalizeRankingPeriod(period string) rankingPeriod {
	switch rankingPeriod(strings.ToLower(strings.TrimSpace(period))) {
	case rankingPeriodWeekly:
		return rankingPeriodWeekly
	case rankingPeriodMonthly:
		return rankingPeriodMonthly
	default:
		return rankingPeriodDaily
	}
}

func normalizeRankingKind(kind int) int {
	if kind < 0 || kind > 3 {
		return 0
	}
	return kind
}

func (c *Client) rankingsFromCache(key string) ([]domain.Movie, bool) {
	c.rankingsMu.Lock()
	defer c.rankingsMu.Unlock()
	entry, ok := c.rankingsCache[key]
	if !ok || time.Now().After(entry.expires) {
		return nil, false
	}
	return entry.movies, true
}

func (c *Client) storeRankings(key string, movies []domain.Movie) {
	c.rankingsMu.Lock()
	defer c.rankingsMu.Unlock()
	if c.rankingsCache == nil {
		c.rankingsCache = make(map[string]cachedRankings)
	}
	c.rankingsCache[key] = cachedRankings{movies: movies, expires: time.Now().Add(rankingsCacheTTL)}
}

// clearRankingsCache drops account-scoped results after a credential change.
func (c *Client) clearRankingsCache() {
	c.rankingsMu.Lock()
	defer c.rankingsMu.Unlock()
	clear(c.rankingsCache)
}
