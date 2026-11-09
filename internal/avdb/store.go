package avdb

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	_ "modernc.org/sqlite"

	"github.com/ppxb/miyabi/internal/domain"
)

// Resource is one row of an AVDB article export (a downloadable resource entry).
type Resource struct {
	Tid           int64  `json:"tid,string"`
	Number        string `json:"number"`
	Title         string `json:"title"`
	PublishDate   string `json:"publish_date"`
	Magnet        string `json:"magnet"`
	PreviewImages string `json:"preview_images"`
	DetailURL     string `json:"detail_url"`
	Size          int64  `json:"size"`
	Section       string `json:"section"`
	Category      string `json:"category"`
	Website       string `json:"website"`
}

// SectionSummary mirrors AVDB's category cards: one entry per section with a
// total count and the per-category breakdown.
type SectionSummary struct {
	Section    string           `json:"section"`
	Count      int64            `json:"count"`
	Categories []CategoryCount  `json:"categories"`
	Sample     string           `json:"sample,omitempty"`
}

// CategoryCount is the per-category tally inside a section.
type CategoryCount struct {
	Name  string `json:"name"`
	Count int64  `json:"count"`
}

// ImportResult reports what an import touched.
type ImportResult struct {
	Imported int64  `json:"imported"`
	Replaced int64  `json:"replaced"`
	Skipped  int64  `json:"skipped"`
	Sections int    `json:"sections"`
	Total    int64  `json:"total"`
	Source   string `json:"source"`
}

// ResourceStore keeps imported AVDB resources in a dedicated SQLite file so the
// ent-managed Miyabi schema is never touched by external data.
type ResourceStore struct {
	db *sql.DB
}

const resourceSchema = `
CREATE TABLE IF NOT EXISTS avdb_resource (
	tid            INTEGER PRIMARY KEY,
	number         TEXT NOT NULL DEFAULT '',
	title          TEXT NOT NULL DEFAULT '',
	publish_date   TEXT NOT NULL DEFAULT '',
	magnet         TEXT NOT NULL DEFAULT '',
	preview_images TEXT NOT NULL DEFAULT '',
	detail_url     TEXT NOT NULL DEFAULT '',
	size           INTEGER NOT NULL DEFAULT 0,
	section        TEXT NOT NULL DEFAULT '',
	category       TEXT NOT NULL DEFAULT '',
	website        TEXT NOT NULL DEFAULT '',
	imported_at    TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_avdb_resource_section ON avdb_resource(section);
CREATE INDEX IF NOT EXISTS idx_avdb_resource_number ON avdb_resource(number);
CREATE INDEX IF NOT EXISTS idx_avdb_resource_title ON avdb_resource(title);
`

// OpenResourceStore opens (and creates) the resource database.
func OpenResourceStore(dataDir string) (*ResourceStore, error) {
	path := filepath.Join(dataDir, "avdb-resources.db")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(resourceSchema); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &ResourceStore{db: db}, nil
}

// Close releases the database handle.
func (s *ResourceStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// empty reports whether nothing has been imported yet.
func (s *ResourceStore) empty(ctx context.Context) bool {
	var n int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM avdb_resource`).Scan(&n); err != nil {
		return true
	}
	return n == 0
}

// ImportZip accepts an AVDB export (.zip containing one CSV) or a bare CSV and
// upserts every row by tid. Rows without a tid are skipped.
func (s *ResourceStore) ImportZip(ctx context.Context, name string, body io.Reader) (ImportResult, error) {
	reader, source, err := csvReaderFrom(name, body)
	if err != nil {
		return ImportResult{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ImportResult{}, err
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.PrepareContext(ctx, `INSERT OR REPLACE INTO avdb_resource
		(tid, number, title, publish_date, magnet, preview_images, detail_url, size, section, category, website, imported_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, datetime('now'))`)
	if err != nil {
		return ImportResult{}, err
	}
	defer func() { _ = stmt.Close() }()

	var result ImportResult
	result.Source = source
	index := map[string]int{}
	batch := 0
	for {
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return result, domain.E(domain.KindInvalid, "CSV 解析失败", err)
		}
		if len(index) == 0 {
			for i, header := range record {
				index[strings.TrimSpace(strings.ToLower(strings.TrimPrefix(header, "\ufeff")))] = i
			}
			continue
		}
		row, ok := rowFromCSV(index, record)
		if !ok {
			result.Skipped++
			continue
		}
		if _, err := stmt.ExecContext(ctx, row.Tid, row.Number, row.Title, row.PublishDate,
			row.Magnet, row.PreviewImages, row.DetailURL, row.Size, row.Section, row.Category, row.Website); err != nil {
			return result, domain.E(domain.KindUpstream, "写入资源失败", err)
		}
		result.Imported++
		batch++
		if batch%5000 == 0 {
			// Keep the transaction short enough for WAL readers.
			if err := tx.Commit(); err != nil {
				return result, err
			}
			if tx, err = s.db.BeginTx(ctx, nil); err != nil {
				return result, err
			}
			if stmt, err = tx.PrepareContext(ctx, `INSERT OR REPLACE INTO avdb_resource
				(tid, number, title, publish_date, magnet, preview_images, detail_url, size, section, category, website, imported_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, datetime('now'))`); err != nil {
				return result, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return result, err
	}

	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM avdb_resource`).Scan(&result.Total); err != nil {
		return result, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT section) FROM avdb_resource`).Scan(&result.Sections); err != nil {
		return result, err
	}
	return result, nil
}

// Sections returns the category cards, sorted by count.
func (s *ResourceStore) Sections(ctx context.Context) ([]SectionSummary, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT section,
		COALESCE(NULLIF(category, ''), ''), COUNT(1) AS n
		FROM avdb_resource
		WHERE section <> ''
		GROUP BY section, COALESCE(NULLIF(category, ''), '')
		ORDER BY n DESC`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	byName := map[string]*SectionSummary{}
	order := make([]string, 0, 32)
	for rows.Next() {
		var section, category string
		var count int64
		if err := rows.Scan(&section, &category, &count); err != nil {
			return nil, err
		}
		entry, ok := byName[section]
		if !ok {
			entry = &SectionSummary{Section: section, Categories: make([]CategoryCount, 0, 4)}
			byName[section] = entry
			order = append(order, section)
		}
		entry.Count += count
		if category != "" {
			entry.Categories = append(entry.Categories, CategoryCount{Name: category, Count: count})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]SectionSummary, 0, len(byName))
	for _, name := range order {
		entry := byName[name]
		sort.Slice(entry.Categories, func(i, j int) bool {
			return entry.Categories[i].Count > entry.Categories[j].Count
		})
		if len(entry.Categories) > 12 {
			entry.Categories = entry.Categories[:12]
		}
		out = append(out, *entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Count > out[j].Count })
	return out, nil
}

// List returns one page of resources, optionally filtered by section and keyword.
func (s *ResourceStore) List(ctx context.Context, section, keyword string, page, limit int) ([]Resource, int64, error) {
	if page < 1 {
		page = 1
	}
	if limit <= 0 || limit > 100 {
		limit = 40
	}
	where := []string{"1=1"}
	args := []any{}
	if strings.TrimSpace(section) != "" {
		where = append(where, "section = ?")
		args = append(args, section)
	}
	if kw := strings.TrimSpace(keyword); kw != "" {
		where = append(where, "(number LIKE ? OR title LIKE ?)")
		like := "%" + kw + "%"
		args = append(args, like, like)
	}
	clause := strings.Join(where, " AND ")

	var total int64
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(1) FROM avdb_resource WHERE "+clause, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	query := "SELECT tid, number, title, publish_date, magnet, preview_images, detail_url, size, section, category, website " +
		"FROM avdb_resource WHERE " + clause + " ORDER BY tid DESC LIMIT ? OFFSET ?"
	args = append(args, limit, (page-1)*limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()

	items := make([]Resource, 0, limit)
	for rows.Next() {
		var item Resource
		if err := rows.Scan(&item.Tid, &item.Number, &item.Title, &item.PublishDate, &item.Magnet,
			&item.PreviewImages, &item.DetailURL, &item.Size, &item.Section, &item.Category, &item.Website); err != nil {
			return nil, 0, err
		}
		items = append(items, item)
	}
	return items, total, rows.Err()
}

func csvReaderFrom(name string, body io.Reader) (*csv.Reader, string, error) {
	lower := strings.ToLower(name)
	if strings.HasSuffix(lower, ".zip") {
		buffered, err := io.ReadAll(body)
		if err != nil {
			return nil, "", domain.E(domain.KindInvalid, "读取上传文件失败", err)
		}
		archive, err := zip.NewReader(bytes.NewReader(buffered), int64(len(buffered)))
		if err != nil {
			return nil, "", domain.E(domain.KindInvalid, "ZIP 解析失败", err)
		}
		for _, file := range archive.File {
			if !strings.HasSuffix(strings.ToLower(file.Name), ".csv") {
				continue
			}
			handle, err := file.Open()
			if err != nil {
				return nil, "", domain.E(domain.KindInvalid, "打开 CSV 失败", err)
			}
			return newCSVReader(handle), file.Name, nil
		}
		return nil, "", domain.E(domain.KindInvalid, "压缩包里没有 CSV", nil)
	}
	return newCSVReader(body), name, nil
}

func newCSVReader(r io.Reader) *csv.Reader {
	reader := csv.NewReader(r)
	reader.FieldsPerRecord = -1
	reader.LazyQuotes = true
	return reader
}

func rowFromCSV(index map[string]int, record []string) (Resource, bool) {
	get := func(key string) string {
		i, ok := index[key]
		if !ok || i >= len(record) {
			return ""
		}
		return strings.TrimSpace(record[i])
	}
	tidText := get("tid")
	if tidText == "" {
		return Resource{}, false
	}
	tid, err := strconv.ParseInt(tidText, 10, 64)
	if err != nil || tid == 0 {
		return Resource{}, false
	}
	var size int64
	if raw := get("size"); raw != "" {
		if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil {
			size = parsed
		}
	}
	return Resource{
		Tid:           tid,
		Number:        get("number"),
		Title:         get("title"),
		PublishDate:   firstNonEmpty(get("publish_date"), get("create_time")),
		Magnet:        get("magnet"),
		PreviewImages: get("preview_images"),
		DetailURL:     get("detail_url"),
		Size:          size,
		Section:       get("section"),
		Category:      get("category"),
		Website:       get("website"),
	}, true
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// Get returns one resource by tid.
func (s *ResourceStore) Get(ctx context.Context, tid int64) (Resource, error) {
	var item Resource
	err := s.db.QueryRowContext(ctx, `SELECT tid, number, title, publish_date, magnet, preview_images,
		detail_url, size, section, category, website FROM avdb_resource WHERE tid = ?`, tid).
		Scan(&item.Tid, &item.Number, &item.Title, &item.PublishDate, &item.Magnet,
			&item.PreviewImages, &item.DetailURL, &item.Size, &item.Section, &item.Category, &item.Website)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Resource{}, domain.E(domain.KindNotFound, "资源不存在", err)
		}
		return Resource{}, err
	}
	return item, nil
}

// Count is a small helper for diagnostics.
func (s *ResourceStore) Count(ctx context.Context) (int64, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("resource store not initialised")
	}
	var n int64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM avdb_resource`).Scan(&n)
	return n, err
}
