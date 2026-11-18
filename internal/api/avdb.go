package api

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/gin-gonic/gin"
	"github.com/ppxb/miyabi/internal/catalogue"
	"github.com/ppxb/miyabi/internal/domain"
	"github.com/ppxb/miyabi/internal/javdb"
)

// RankingsProvider supplies one upstream ranking list.
type RankingsProvider interface {
	Rankings(ctx context.Context, period string, kind int) ([]domain.Movie, error)
}

// RankingsSource supplies ranking lists as Miyabi catalogue movies.
type RankingsSource interface {
	RankingsProvider
	Enabled() bool
}

type rankingsWithFallback struct {
	primary  RankingsProvider
	fallback RankingsSource
	logger   *slog.Logger
}

// NewRankingsSource prioritizes JavDB and uses AVDB when JavDB is unavailable.
func NewRankingsSource(primary RankingsProvider, fallback RankingsSource, logger *slog.Logger) RankingsSource {
	if logger == nil {
		logger = slog.Default()
	}
	return &rankingsWithFallback{primary: primary, fallback: fallback, logger: logger}
}

func (source *rankingsWithFallback) Enabled() bool {
	return source.primary != nil || source.fallback != nil && source.fallback.Enabled()
}

func (source *rankingsWithFallback) Rankings(ctx context.Context, period string, kind int) ([]domain.Movie, error) {
	var primaryErr error
	if source.primary != nil {
		movies, err := source.primary.Rankings(ctx, period, kind)
		if err == nil {
			source.logServed(ctx, "javdb", period, kind)
			return movies, nil
		}
		primaryErr = err
		source.logger.WarnContext(ctx, "JavDB rankings failed; trying AVDB",
			"error", err, "auth_failure", javdb.IsAuthFailure(err))
	}

	if source.fallback != nil && source.fallback.Enabled() {
		movies, fallbackErr := source.fallback.Rankings(ctx, period, kind)
		if fallbackErr == nil {
			source.logServed(ctx, "avdb", period, kind)
			return movies, nil
		}
		if primaryErr != nil {
			return nil, domain.E(domain.KindUpstream, fmt.Sprintf(
				"JavDB 榜单失败：%v；AVDB 榜单失败：%v", primaryErr, fallbackErr,
			), nil)
		}
		return nil, fallbackErr
	}
	return nil, primaryErr
}

func (source *rankingsWithFallback) logServed(ctx context.Context, name, period string, kind int) {
	source.logger.InfoContext(ctx, "rankings served by "+name,
		"period", period, "type", kind)
}

type rankingsQuery struct {
	Period string `form:"period,default=daily"`
	Type   int    `form:"type,default=0"`
}

// discoverRankingsHandler returns ranking movies shaped exactly like
// /discover/movies so the frontend can reuse its cards and grid.
func discoverRankingsHandler(source RankingsSource) gin.HandlerFunc {
	return func(c *gin.Context) {
		if source == nil || !source.Enabled() {
			respond(c, nil, domain.E(domain.KindInvalid, "未配置榜单数据源", nil))
			return
		}
		query, ok := bindQuery[rankingsQuery](c)
		if !ok {
			return
		}
		movies, err := source.Rankings(c.Request.Context(), query.Period, query.Type)
		if err != nil {
			respond(c, nil, err)
			return
		}
		results := make([]catalogue.Movie, 0, len(movies))
		for _, movie := range movies {
			results = append(results, catalogue.Movie{
				Movie:         movie,
				ReleaseStatus: catalogue.ReleaseStatus("released"),
			})
		}
		c.Header("Cache-Control", "no-store")
		respond(c, results, nil)
	}
}
