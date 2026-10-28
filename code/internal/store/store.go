// Package store persists searches, documents, publications and saved
// references in SQLite.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"citewise/internal/models"

	_ "modernc.org/sqlite" // pure-Go SQLite driver
)

// ErrNotFound is returned when a record does not exist.
var ErrNotFound = errors.New("not found")

// Store wraps the SQLite database.
type Store struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS searches (
	id              INTEGER PRIMARY KEY AUTOINCREMENT,
	topic           TEXT    NOT NULL,
	description     TEXT    NOT NULL DEFAULT '',
	search_type     TEXT    NOT NULL CHECK (search_type IN ('title', 'essay')),
	citation_style  TEXT    NOT NULL CHECK (citation_style IN ('apa', 'harvard')),
	requested_count INTEGER NOT NULL DEFAULT 10,
	year_from       INTEGER NOT NULL DEFAULT 0,
	year_to         INTEGER NOT NULL DEFAULT 0,
	ai_used         INTEGER NOT NULL DEFAULT 0,
	queries_json    TEXT    NOT NULL DEFAULT '[]',
	claims_json     TEXT    NOT NULL DEFAULT '[]',
	notice          TEXT    NOT NULL DEFAULT '',
	created_at      TEXT    NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_searches_created ON searches(created_at);

CREATE TABLE IF NOT EXISTS documents (
	id           INTEGER PRIMARY KEY AUTOINCREMENT,
	search_id    INTEGER REFERENCES searches(id) ON DELETE SET NULL,
	filename     TEXT    NOT NULL,
	size_bytes   INTEGER NOT NULL,
	status       TEXT    NOT NULL CHECK (status IN ('received', 'processed', 'failed')),
	error        TEXT    NOT NULL DEFAULT '',
	created_at   TEXT    NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_documents_search ON documents(search_id);

CREATE TABLE IF NOT EXISTS publications (
	id           INTEGER PRIMARY KEY AUTOINCREMENT,
	dedup_key    TEXT    NOT NULL UNIQUE,
	doi          TEXT    NOT NULL DEFAULT '',
	title        TEXT    NOT NULL,
	authors_json TEXT    NOT NULL DEFAULT '[]',
	year         INTEGER NOT NULL DEFAULT 0,
	venue        TEXT    NOT NULL DEFAULT '',
	volume       TEXT    NOT NULL DEFAULT '',
	issue        TEXT    NOT NULL DEFAULT '',
	pages        TEXT    NOT NULL DEFAULT '',
	publisher    TEXT    NOT NULL DEFAULT '',
	pub_type     TEXT    NOT NULL DEFAULT '',
	url          TEXT    NOT NULL DEFAULT '',
	abstract     TEXT    NOT NULL DEFAULT '',
	source       TEXT    NOT NULL DEFAULT '',
	created_at   TEXT    NOT NULL,
	updated_at   TEXT    NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_publications_doi ON publications(doi);

CREATE TABLE IF NOT EXISTS search_results (
	id             INTEGER PRIMARY KEY AUTOINCREMENT,
	search_id      INTEGER NOT NULL REFERENCES searches(id) ON DELETE CASCADE,
	publication_id INTEGER NOT NULL REFERENCES publications(id) ON DELETE CASCADE,
	rank           INTEGER NOT NULL,
	score          REAL    NOT NULL DEFAULT 0,
	claim_index    INTEGER NOT NULL DEFAULT -1,
	claim          TEXT    NOT NULL DEFAULT '',
	section        TEXT    NOT NULL DEFAULT '',
	rationale      TEXT    NOT NULL DEFAULT '',
	approved       INTEGER NOT NULL DEFAULT 0 CHECK (approved IN (-1, 0, 1)),
	UNIQUE (search_id, publication_id, claim_index)
);
CREATE INDEX IF NOT EXISTS idx_results_search ON search_results(search_id, claim_index, rank);
CREATE INDEX IF NOT EXISTS idx_results_publication ON search_results(publication_id);

CREATE TABLE IF NOT EXISTS saved_references (
	id             INTEGER PRIMARY KEY AUTOINCREMENT,
	publication_id INTEGER NOT NULL UNIQUE REFERENCES publications(id) ON DELETE CASCADE,
	created_at     TEXT    NOT NULL
);
`

// Open opens (creating if necessary) the database at path and applies the
// schema.
func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" && path != ":memory:" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create database directory: %w", err)
		}
	}
	dsn := "file:" + filepath.ToSlash(path) +
		"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	// A single connection avoids SQLITE_BUSY between writers; the workload is
	// small and local.
	db.SetMaxOpenConns(1)
	db.SetConnMaxIdleTime(0)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := db.ExecContext(ctx, schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("initialize schema: %w", err)
	}
	return &Store{db: db}, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// Ping checks database connectivity.
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

func now() string { return time.Now().UTC().Format(time.RFC3339) }

// Search is a persisted search request.
type Search struct {
	ID             int64
	Topic          string
	Description    string
	Type           string // "title" or "essay"
	Style          string // "apa" or "harvard"
	RequestedCount int
	YearFrom       int
	YearTo         int
	AIUsed         bool
	Queries        []string
	Claims         []Claim // essay searches: AI-paraphrased claims (never essay text)
	Notice         string
	CreatedAt      time.Time
}

// Claim is an essay claim identified during analysis.
type Claim struct {
	Index   int    `json:"index"`
	Section int    `json:"section"`
	Text    string `json:"text"`
}

// ResultInput associates a publication with a search.
type ResultInput struct {
	Publication models.Publication
	Score       float64
	ClaimIndex  int // -1 for title searches
	Claim       string
	Section     string
	Rationale   string
}

// Result is a stored search result joined with its publication.
type Result struct {
	ID          int64
	Rank        int
	Score       float64
	ClaimIndex  int
	Claim       string
	Section     string
	Rationale   string
	Approved    int
	Saved       bool
	Publication models.Publication
}

// SaveSearch stores a search and its results in one transaction and returns
// the search ID.
func (s *Store) SaveSearch(ctx context.Context, srch Search, results []ResultInput) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	queries, _ := json.Marshal(srch.Queries)
	if srch.Queries == nil {
		queries = []byte("[]")
	}
	claims, _ := json.Marshal(srch.Claims)
	if srch.Claims == nil {
		claims = []byte("[]")
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO searches
		(topic, description, search_type, citation_style, requested_count, year_from, year_to, ai_used, queries_json, claims_json, notice, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		srch.Topic, srch.Description, srch.Type, srch.Style, srch.RequestedCount,
		srch.YearFrom, srch.YearTo, boolInt(srch.AIUsed), string(queries), string(claims), srch.Notice, now())
	if err != nil {
		return 0, fmt.Errorf("insert search: %w", err)
	}
	searchID, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	rankByClaim := map[int]int{}
	for _, r := range results {
		pubID, err := upsertPublication(ctx, tx, r.Publication)
		if err != nil {
			return 0, err
		}
		rankByClaim[r.ClaimIndex]++
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO search_results
			(search_id, publication_id, rank, score, claim_index, claim, section, rationale)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			searchID, pubID, rankByClaim[r.ClaimIndex], r.Score, r.ClaimIndex, r.Claim, r.Section, r.Rationale); err != nil {
			return 0, fmt.Errorf("insert result: %w", err)
		}
	}
	return searchID, tx.Commit()
}

// DedupKey returns the storage identity of a publication.
func DedupKey(p models.Publication) string {
	if doi := models.NormalizeDOI(p.DOI); doi != "" {
		return "doi:" + doi
	}
	first := ""
	if len(p.Authors) > 0 {
		first = models.NormalizeTitle(p.Authors[0].Family + p.Authors[0].Literal)
	}
	return fmt.Sprintf("t:%s|%d|%s", models.NormalizeTitle(p.Title), p.Year, first)
}

func upsertPublication(ctx context.Context, tx *sql.Tx, p models.Publication) (int64, error) {
	if strings.TrimSpace(p.Title) == "" {
		return 0, errors.New("publication without title")
	}
	authors, err := json.Marshal(p.Authors)
	if err != nil {
		return 0, err
	}
	if p.Authors == nil {
		authors = []byte("[]")
	}
	ts := now()
	var id int64
	err = tx.QueryRowContext(ctx, `INSERT INTO publications
		(dedup_key, doi, title, authors_json, year, venue, volume, issue, pages, publisher, pub_type, url, abstract, source, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(dedup_key) DO UPDATE SET
			title = excluded.title,
			authors_json = CASE WHEN excluded.authors_json != '[]' THEN excluded.authors_json ELSE publications.authors_json END,
			year = CASE WHEN excluded.year != 0 THEN excluded.year ELSE publications.year END,
			venue = COALESCE(NULLIF(excluded.venue, ''), publications.venue),
			volume = COALESCE(NULLIF(excluded.volume, ''), publications.volume),
			issue = COALESCE(NULLIF(excluded.issue, ''), publications.issue),
			pages = COALESCE(NULLIF(excluded.pages, ''), publications.pages),
			publisher = COALESCE(NULLIF(excluded.publisher, ''), publications.publisher),
			pub_type = COALESCE(NULLIF(excluded.pub_type, ''), publications.pub_type),
			url = COALESCE(NULLIF(excluded.url, ''), publications.url),
			abstract = COALESCE(NULLIF(excluded.abstract, ''), publications.abstract),
			source = excluded.source,
			updated_at = excluded.updated_at
		RETURNING id`,
		DedupKey(p), models.NormalizeDOI(p.DOI), p.Title, string(authors), p.Year, p.Venue, p.Volume, p.Issue,
		p.Pages, p.Publisher, p.Type, p.URL, p.Abstract, p.Source, ts, ts).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("upsert publication: %w", err)
	}
	return id, nil
}

const searchCols = `id, topic, description, search_type, citation_style, requested_count, year_from, year_to, ai_used, queries_json, claims_json, notice, created_at`

func scanSearch(sc interface{ Scan(...any) error }) (Search, error) {
	var (
		s       Search
		ai      int
		queries string
		claims  string
		created string
	)
	if err := sc.Scan(&s.ID, &s.Topic, &s.Description, &s.Type, &s.Style, &s.RequestedCount,
		&s.YearFrom, &s.YearTo, &ai, &queries, &claims, &s.Notice, &created); err != nil {
		return s, err
	}
	s.AIUsed = ai == 1
	_ = json.Unmarshal([]byte(queries), &s.Queries)
	_ = json.Unmarshal([]byte(claims), &s.Claims)
	s.CreatedAt, _ = time.Parse(time.RFC3339, created)
	return s, nil
}

// GetSearch returns a search by ID.
func (s *Store) GetSearch(ctx context.Context, id int64) (Search, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+searchCols+` FROM searches WHERE id = ?`, id)
	srch, err := scanSearch(row)
	if errors.Is(err, sql.ErrNoRows) {
		return srch, ErrNotFound
	}
	return srch, err
}

// ListSearches returns the most recent searches.
func (s *Store) ListSearches(ctx context.Context, limit int) ([]Search, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+searchCols+` FROM searches ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Search
	for rows.Next() {
		srch, err := scanSearch(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, srch)
	}
	return out, rows.Err()
}

const pubCols = `p.id, p.doi, p.title, p.authors_json, p.year, p.venue, p.volume, p.issue, p.pages, p.publisher, p.pub_type, p.url, p.abstract, p.source`

func pubDest(p *models.Publication, authors *string) []any {
	return []any{&p.ID, &p.DOI, &p.Title, authors, &p.Year, &p.Venue, &p.Volume, &p.Issue, &p.Pages, &p.Publisher, &p.Type, &p.URL, &p.Abstract, &p.Source}
}

// ListResults returns the results of a search ordered by claim and rank.
func (s *Store) ListResults(ctx context.Context, searchID int64) ([]Result, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT r.id, r.rank, r.score, r.claim_index, r.claim, r.section, r.rationale, r.approved,
			(sr.id IS NOT NULL), `+pubCols+`
		FROM search_results r
		JOIN publications p ON p.id = r.publication_id
		LEFT JOIN saved_references sr ON sr.publication_id = p.id
		WHERE r.search_id = ?
		ORDER BY r.claim_index, r.rank`, searchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Result
	for rows.Next() {
		var r Result
		var authors string
		dest := append([]any{&r.ID, &r.Rank, &r.Score, &r.ClaimIndex, &r.Claim, &r.Section, &r.Rationale, &r.Approved, &r.Saved},
			pubDest(&r.Publication, &authors)...)
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(authors), &r.Publication.Authors)
		out = append(out, r)
	}
	return out, rows.Err()
}

// SetApproval marks an essay suggestion as approved (1), rejected (-1) or
// pending (0).
func (s *Store) SetApproval(ctx context.Context, resultID int64, value int) error {
	if value < -1 || value > 1 {
		return errors.New("invalid approval value")
	}
	res, err := s.db.ExecContext(ctx, `UPDATE search_results SET approved = ? WHERE id = ?`, value, resultID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ResultPublicationID returns the publication referenced by a search result.
func (s *Store) ResultPublicationID(ctx context.Context, resultID int64) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `SELECT publication_id FROM search_results WHERE id = ?`, resultID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return id, err
}

// SaveReference adds a publication to the saved references.
func (s *Store) SaveReference(ctx context.Context, publicationID int64) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO saved_references (publication_id, created_at)
		SELECT id, ? FROM publications WHERE id = ?
		ON CONFLICT(publication_id) DO NOTHING`, now(), publicationID)
	if err != nil {
		return err
	}
	var exists int
	err = s.db.QueryRowContext(ctx, `SELECT 1 FROM publications WHERE id = ?`, publicationID).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// AddReference stores a publication (merging with any existing record for
// the same work) and saves it. It returns the publication ID and whether it
// was already saved.
func (s *Store) AddReference(ctx context.Context, p models.Publication) (id int64, alreadySaved bool, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, false, err
	}
	defer tx.Rollback()
	if id, err = upsertPublication(ctx, tx, p); err != nil {
		return 0, false, err
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO saved_references (publication_id, created_at) VALUES (?, ?)
		ON CONFLICT(publication_id) DO NOTHING`, id, now())
	if err != nil {
		return 0, false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, false, err
	}
	return id, n == 0, tx.Commit()
}

// RemoveReference removes a publication from the saved references.
func (s *Store) RemoveReference(ctx context.Context, publicationID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM saved_references WHERE publication_id = ?`, publicationID)
	return err
}

// SavedReference is a saved publication.
type SavedReference struct {
	SavedAt     time.Time
	Publication models.Publication
}

// ListSaved returns all saved references, newest first.
func (s *Store) ListSaved(ctx context.Context) ([]SavedReference, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT sr.created_at, `+pubCols+`
		FROM saved_references sr JOIN publications p ON p.id = sr.publication_id
		ORDER BY sr.id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SavedReference
	for rows.Next() {
		var r SavedReference
		var created, authors string
		if err := rows.Scan(append([]any{&created}, pubDest(&r.Publication, &authors)...)...); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(authors), &r.Publication.Authors)
		r.SavedAt, _ = time.Parse(time.RFC3339, created)
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetPublications returns the publications with the given IDs, in the order
// requested. Unknown IDs are skipped.
func (s *Store) GetPublications(ctx context.Context, ids []int64) ([]models.Publication, error) {
	out := make([]models.Publication, 0, len(ids))
	seen := map[int64]bool{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		var p models.Publication
		var authors string
		err := s.db.QueryRowContext(ctx, `SELECT `+pubCols+` FROM publications p WHERE p.id = ?`, id).Scan(pubDest(&p, &authors)...)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(authors), &p.Authors)
		out = append(out, p)
	}
	return out, nil
}

// CreateDocument records an uploaded document's metadata (never its content).
func (s *Store) CreateDocument(ctx context.Context, filename string, size int64) (int64, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO documents (filename, size_bytes, status, created_at) VALUES (?, ?, 'received', ?)`,
		filename, size, now())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateDocument sets a document's processing status and associated search.
func (s *Store) UpdateDocument(ctx context.Context, id int64, status string, searchID int64, errMsg string) error {
	var sid any
	if searchID > 0 {
		sid = searchID
	}
	_, err := s.db.ExecContext(ctx, `UPDATE documents SET status = ?, search_id = ?, error = ? WHERE id = ?`, status, sid, errMsg, id)
	return err
}

// Document is an uploaded document's metadata.
type Document struct {
	ID       int64
	SearchID int64
	Filename string
	Size     int64
	Status   string
	Error    string
}

// DocumentForSearch returns the document associated with a search, if any.
func (s *Store) DocumentForSearch(ctx context.Context, searchID int64) (Document, error) {
	var d Document
	var sid sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT id, search_id, filename, size_bytes, status, error FROM documents WHERE search_id = ?`, searchID).
		Scan(&d.ID, &sid, &d.Filename, &d.Size, &d.Status, &d.Error)
	if errors.Is(err, sql.ErrNoRows) {
		return d, ErrNotFound
	}
	d.SearchID = sid.Int64
	return d, err
}

// GetDocument returns a document by ID.
func (s *Store) GetDocument(ctx context.Context, id int64) (Document, error) {
	var d Document
	var sid sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT id, search_id, filename, size_bytes, status, error FROM documents WHERE id = ?`, id).
		Scan(&d.ID, &sid, &d.Filename, &d.Size, &d.Status, &d.Error)
	if errors.Is(err, sql.ErrNoRows) {
		return d, ErrNotFound
	}
	d.SearchID = sid.Int64
	return d, err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
