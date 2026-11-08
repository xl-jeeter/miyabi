// Package avdb talks to an external AVDB instance and exposes its discovery
// data as Miyabi-domain movies. AVDB is treated as a read-only source: this
// package never writes to it and never exposes its credentials upward.
package avdb

import (
	"strings"

	"github.com/ppxb/miyabi/internal/domain"
)

// envelope matches AVDB's uniform response wrapper.
type envelope[T any] struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    T      `json:"data"`
}

type rankingData struct {
	Movies []wireMovie `json:"movies"`
}

// wireMovie mirrors the subset of AVDB's movie payload that Miyabi renders.
// Unknown fields are deliberately dropped instead of being forwarded to the UI.
type wireMovie struct {
	ID             string        `json:"id"`
	Number         string        `json:"number"`
	Title          string        `json:"title"`
	OriginTitle    string        `json:"origin_title"`
	ThumbURL       string        `json:"thumb_url"`
	CoverURL       string        `json:"cover_url"`
	Duration       int           `json:"duration"`
	MagnetsCount   int           `json:"magnets_count"`
	ReleaseDate    string        `json:"release_date"`
	HasCnsub       bool          `json:"has_cnsub"`
	HasPreviewImg  bool          `json:"has_preview_images"`
	HasPreviewVid  bool          `json:"has_preview_video"`
	PreviewImages  []wirePreview `json:"preview_images"`
	PreviewVideo   string        `json:"preview_video"`
	CanPlay        bool          `json:"can_play"`
	RankingScore   float64       `json:"ranking_score"`
	RankingTypeCnt int           `json:"ranking_type_count"`
}

type wirePreview struct {
	ThumbURL string `json:"thumb_url"`
	LargeURL string `json:"large_url"`
}

// toDomain converts an AVDB record into Miyabi's movie model.
func (w wireMovie) toDomain() domain.Movie {
	previews := make([]domain.PreviewImage, 0, len(w.PreviewImages))
	for _, preview := range w.PreviewImages {
		thumbnail := strings.TrimSpace(preview.ThumbURL)
		original := strings.TrimSpace(preview.LargeURL)
		if thumbnail == "" && original == "" {
			continue
		}
		if thumbnail == "" {
			thumbnail = original
		}
		if original == "" {
			original = thumbnail
		}
		previews = append(previews, domain.PreviewImage{Thumbnail: thumbnail, Original: original})
	}

	return domain.Movie{
		Sources: []domain.SourceID{{
			Provider: "avdb",
			ID:       w.ID,
		}},
		ID:            w.ID,
		Code:          w.Number,
		Title:         w.Title,
		OriginTitle:   w.OriginTitle,
		ReleaseDate:   w.ReleaseDate,
		Duration:      w.Duration,
		Thumbnail:     w.ThumbURL,
		Cover:         w.CoverURL,
		PreviewImages: previews,
		PreviewVideo:  w.PreviewVideo,
		MagnetsCount:  w.MagnetsCount,
		HasSubtitle:   w.HasCnsub,
		HasPreview:    w.HasPreviewImg || w.HasPreviewVid || len(previews) > 0,
		Actors:        []domain.Actor{},
		Tags:          []domain.Tag{},
	}
}
