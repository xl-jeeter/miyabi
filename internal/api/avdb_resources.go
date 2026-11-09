package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/ppxb/miyabi/internal/avdb"
	"github.com/ppxb/miyabi/internal/netx"
	"github.com/ppxb/miyabi/internal/domain"
)

// ResourceManager exposes the imported AVDB resource library.
type ResourceManager interface {
	Sections(ctx context.Context) ([]avdb.SectionSummary, error)
	List(ctx context.Context, section, keyword string, page, limit int) ([]avdb.Resource, int64, error)
	Get(ctx context.Context, tid int64) (avdb.Resource, error)
	ImportZip(ctx context.Context, name string, body io.Reader) (avdb.ImportResult, error)
}

type resourceListQuery struct {
	Section string `form:"section"`
	Query   string `form:"q"`
	Page    int    `form:"page,default=1" binding:"min=1"`
	Limit   int    `form:"limit,default=40" binding:"min=1,max=100"`
}

func avdbSectionsHandler(resources ResourceManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		if resources == nil {
			respond(c, []any{}, nil)
			return
		}
		sections, err := resources.Sections(c.Request.Context())
		respond(c, sections, err)
	}
}

func avdbResourcesHandler(resources ResourceManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		if resources == nil {
			respond(c, nil, domain.E(domain.KindInvalid, "资源库未初始化", nil))
			return
		}
		query, ok := bindQuery[resourceListQuery](c)
		if !ok {
			return
		}
		items, total, err := resources.List(c.Request.Context(), query.Section, query.Query, query.Page, query.Limit)
		if err != nil {
			respond(c, nil, err)
			return
		}
		respond(c, gin.H{"items": items, "total": total, "page": query.Page, "limit": query.Limit}, nil)
	}
}

// avdbImportHandler accepts an AVDB export (.zip with one CSV, or a bare CSV)
// and upserts it into the local resource library.
func avdbImportHandler(resources ResourceManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		if resources == nil {
			respond(c, nil, domain.E(domain.KindInvalid, "资源库未初始化", nil))
			return
		}
		file, err := c.FormFile("file")
		if err != nil {
			respond(c, nil, domain.E(domain.KindInvalid, "请上传导出文件（.zip 或 .csv）", err))
			return
		}
		ext := strings.ToLower(filepath.Ext(file.Filename))
		if ext != ".zip" && ext != ".csv" {
			respond(c, nil, domain.E(domain.KindInvalid, "只支持 .zip 或 .csv", nil))
			return
		}

		source, err := file.Open()
		if err != nil {
			respond(c, nil, domain.E(domain.KindInvalid, "读取上传文件失败", err))
			return
		}
		defer func() { _ = source.Close() }()

		result, err := resources.ImportZip(c.Request.Context(), file.Filename, source)
		if err != nil {
			respond(c, nil, err)
			return
		}
		respond(c, result, nil)
	}
}

type resourceURI struct {
	Tid string `uri:"tid" binding:"required"`
}

// btihFromMagnet extracts the BTIH from a magnet URI (hex or base32).
func btihFromMagnet(magnet string) string {
	lower := strings.ToLower(magnet)
	index := strings.Index(lower, "btih:")
	if index < 0 {
		return ""
	}
	rest := lower[index+5:]
	if end := strings.IndexAny(rest, "&?#"); end >= 0 {
		rest = rest[:end]
	}
	return rest
}

// avdbResourceOfflineHandler sends an imported resource's magnet straight to
// 115 through the offline pipeline, without requiring a JavDB catalogue entry.
func avdbResourceOfflineHandler(resources ResourceManager, offline OfflineManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		if resources == nil || offline == nil {
			respond(c, nil, domain.E(domain.KindInvalid, "离线下载或资源库未就绪", nil))
			return
		}
		uri, ok := bindURI[resourceURI](c)
		if !ok {
			return
		}
		tid, err := strconv.ParseInt(uri.Tid, 10, 64)
		if err != nil {
			respond(c, nil, domain.E(domain.KindInvalid, "资源 ID 无效", err))
			return
		}
		resource, err := resources.Get(c.Request.Context(), tid)
		if err != nil {
			respond(c, nil, err)
			return
		}
		hash := btihFromMagnet(resource.Magnet)
		if hash == "" {
			respond(c, nil, domain.E(domain.KindInvalid, "该资源没有可用的磁力链接", nil))
			return
		}
		submission, err := offline.AddByHash(c.Request.Context(), resource.Number, hash)
		accepted(c, submission, err)
	}
}

const browserUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36"

// avdbResourceImageHandler proxies images from AVDB resource hosts. The generic
// /api/image endpoint is restricted to JavDB CDNs, and these hosts need a
// browser User-Agent plus a same-host Referer to avoid 403 hotlink protection.
func avdbResourceImageHandler(network NetworkManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		raw := strings.TrimSpace(c.Query("url"))
		parsed, err := url.Parse(raw)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			respond(c, nil, domain.E(domain.KindInvalid, "图片地址无效", nil))
			return
		}

		var manager *netx.ProxyManager
		if network != nil {
			manager = network.ProxyManager()
		}
		client := &http.Client{Transport: netx.NewTransport(manager), Timeout: 25 * time.Second}

		request, err := http.NewRequestWithContext(c.Request.Context(), http.MethodGet, parsed.String(), nil)
		if err != nil {
			respond(c, nil, domain.E(domain.KindInvalid, "图片请求构造失败", err))
			return
		}
		request.Header.Set("User-Agent", browserUserAgent)
		request.Header.Set("Referer", parsed.Scheme+"://"+parsed.Host+"/")
		request.Header.Set("Accept", "image/avif,image/webp,image/*,*/*;q=0.8")

		response, err := client.Do(request)
		if err != nil {
			respond(c, nil, domain.E(domain.KindUpstream, "图片下载失败", err))
			return
		}
		defer func() { _ = response.Body.Close() }()
		if response.StatusCode != http.StatusOK {
			respond(c, nil, domain.E(domain.KindUpstream, fmt.Sprintf("图片源返回 %d", response.StatusCode), nil))
			return
		}

		body, err := io.ReadAll(io.LimitReader(response.Body, 12<<20))
		if err != nil {
			respond(c, nil, domain.E(domain.KindUpstream, "图片读取失败", err))
			return
		}
		contentType := response.Header.Get("Content-Type")
		if contentType == "" {
			contentType = http.DetectContentType(body)
		}
		c.Header("Cache-Control", "public, max-age=86400")
		c.Data(http.StatusOK, contentType, body)
	}
}
