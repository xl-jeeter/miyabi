package api

import (
	"context"

	"github.com/gin-gonic/gin"
	"github.com/ppxb/miyabi/internal/catalogue"
	"github.com/ppxb/miyabi/internal/domain"
)

// RankingsSource supplies AVDB ranking lists as Miyabi catalogue movies.
type RankingsSource interface {
	Enabled() bool
	Rankings(ctx context.Context, period string, kind int) ([]domain.Movie, error)
}

type rankingsQuery struct {
	Period string `form:"period,default=daily" binding:"oneof=daily weekly monthly"`
	Type   int    `form:"type,default=0" binding:"min=0,max=3"`
}

// discoverRankingsHandler returns ranking movies shaped exactly like
// /discover/movies so the frontend can reuse its cards and grid.
func discoverRankingsHandler(source RankingsSource) gin.HandlerFunc {
	return func(c *gin.Context) {
		if source == nil || !source.Enabled() {
			respond(c, nil, domain.E(domain.KindInvalid, "未配置 AVDB 数据源（MIYABI_AVDB_URL）", nil))
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
