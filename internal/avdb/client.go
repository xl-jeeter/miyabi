package avdb

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/ppxb/miyabi/internal/domain"
	"github.com/ppxb/miyabi/internal/netx"
)

// RankingPeriod is the supported ranking window.
type RankingPeriod string

const (
	PeriodDaily   RankingPeriod = "daily"
	PeriodWeekly  RankingPeriod = "weekly"
	PeriodMonthly RankingPeriod = "monthly"
)

// requestTimeout is generous on purpose: AVDB scrapes upstream JavDB on a cold
// cache, and a first ranking request can legitimately take tens of seconds.
const requestTimeout = 45 * time.Second

// cacheTTL keeps repeat UI navigation from hammering the AVDB instance; the
// source itself already caches upstream JavDB responses.
const cacheTTL = 5 * time.Minute

// Options configures the read-only AVDB client.
type Options struct {
	BaseURL string
	APIKey  string
	Proxy   *netx.ProxyManager
}

// Client is a read-only AVDB API client.
type Client struct {
	baseURL string
	apiKey  string
	http    *resty.Client

	mu    sync.Mutex
	cache map[string]cachedRanking
}

type cachedRanking struct {
	movies  []domain.Movie
	expires time.Time
}

// New returns a client, or nil when no base URL is configured.
func New(options Options) *Client {
	base := strings.TrimRight(strings.TrimSpace(options.BaseURL), "/")
	if base == "" {
		return nil
	}
	return &Client{
		baseURL: base,
		apiKey:  strings.TrimSpace(options.APIKey),
		http: netx.NewRestyClient(options.Proxy, netx.RestyOptions{
			Timeout: requestTimeout,
		}),
		cache: make(map[string]cachedRanking),
	}
}

// Enabled reports whether the integration is configured.
func (c *Client) Enabled() bool { return c != nil }

// Rankings fetches one AVDB ranking list. An unknown period falls back to daily.
func (c *Client) Rankings(ctx context.Context, period string, kind int) ([]domain.Movie, error) {
	if c == nil {
		return nil, domain.E(domain.KindInvalid, "AVDB 数据源未配置", nil)
	}
	normalized, ok := normalizePeriod(period)
	if !ok {
		normalized = PeriodDaily
	}
	if kind < 0 || kind > 3 {
		kind = 0
	}

	cacheKey := fmt.Sprintf("%s|%d", normalized, kind)
	if movies, hit := c.fromCache(cacheKey); hit {
		return movies, nil
	}

	var payload envelope[rankingData]
	request := c.http.R().
		SetContext(ctx).
		SetHeader("Accept", "application/json").
		SetQueryParam("type", fmt.Sprintf("%d", kind)).
		SetQueryParam("period", string(normalized)).
		SetResult(&payload)
	if c.apiKey != "" {
		request.SetHeader("X-API-Key", c.apiKey)
	}

	response, err := request.Get(c.baseURL + "/api/v1/javdb/rankings")
	if err != nil {
		return nil, domain.E(domain.KindUpstream, "连接 AVDB 失败", err)
	}
	if response.StatusCode() != http.StatusOK {
		return nil, domain.E(domain.KindUpstream,
			fmt.Sprintf("AVDB 返回 %d", response.StatusCode()), nil)
	}
	if payload.Code != 0 {
		return nil, domain.E(domain.KindUpstream,
			fmt.Sprintf("AVDB 错误：%s", payload.Message), nil)
	}

	movies := make([]domain.Movie, 0, len(payload.Data.Movies))
	for _, item := range payload.Data.Movies {
		movie := item.toDomain()
		if movie.ID == "" {
			continue
		}
		movies = append(movies, movie)
	}

	c.store(cacheKey, movies)
	return movies, nil
}

func normalizePeriod(period string) (RankingPeriod, bool) {
	switch RankingPeriod(strings.ToLower(strings.TrimSpace(period))) {
	case PeriodDaily:
		return PeriodDaily, true
	case PeriodWeekly:
		return PeriodWeekly, true
	case PeriodMonthly:
		return PeriodMonthly, true
	default:
		return "", false
	}
}

func (c *Client) fromCache(key string) ([]domain.Movie, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.cache[key]
	if !ok || time.Now().After(entry.expires) {
		return nil, false
	}
	return entry.movies, true
}

func (c *Client) store(key string, movies []domain.Movie) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cache[key] = cachedRanking{movies: movies, expires: time.Now().Add(cacheTTL)}
}
