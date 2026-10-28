package web

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"citewise/internal/ai"
	"citewise/internal/config"
	"citewise/internal/essay"
	"citewise/internal/models"
	"citewise/internal/search"
	"citewise/internal/store"

	_ "modernc.org/sqlite"
)

type fakeSearcher struct {
	res search.Result
	err error
	got search.Request
}

func (f *fakeSearcher) Find(_ context.Context, req search.Request) (search.Result, error) {
	f.got = req
	return f.res, f.err
}

type fakeEssays struct {
	rep     essay.Report
	err     error
	gotText string
}

func (f *fakeEssays) Analyze(_ context.Context, text string, _, _ int) (essay.Report, error) {
	f.gotText = text
	return f.rep, f.err
}

type env struct {
	srv     *Server
	h       http.Handler
	store   *store.Store
	dbPath  string
	tempDir string
	search  *fakeSearcher
	essays  *fakeEssays
	doi     *fakeDOI
}

func newEnv(t *testing.T, aiKey string) *env {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	tempDir := filepath.Join(dir, "uploads")
	os.MkdirAll(tempDir, 0o755)
	e := &env{store: st, dbPath: dbPath, tempDir: tempDir, search: &fakeSearcher{res: searchResult()}, essays: &fakeEssays{}, doi: &fakeDOI{}}
	cfg := config.Config{OpenAIKey: aiKey, MaxUploadMB: 1, TempDir: tempDir, Port: "8080"}
	opts := Options{Config: cfg, Store: st, Searcher: e.search, DOI: e.doi, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if aiKey != "" {
		opts.Essays = e.essays
	}
	e.srv, err = New(opts)
	if err != nil {
		t.Fatal(err)
	}
	e.h = e.srv.Handler()
	return e
}

func searchResult() search.Result {
	return search.Result{
		Queries: []string{"climate agriculture"},
		Items: []search.Ranked{
			{Publication: models.Publication{DOI: "10.1/crop", Title: "Climate change and crop yields", Year: 2018,
				Authors: []models.Author{{Given: "David", Family: "Lobell"}}, Venue: "Nature Climate Change", Volume: "8", Pages: "1-5", Source: "crossref"}, Score: 3},
			{Publication: models.Publication{Title: "Adaptation without DOI", Year: 2010, Source: "openalex", URL: "https://openalex.org/W1"}, Score: 1},
		},
		Warnings: []string{"OpenAlex could not be reached, so its results are missing."},
	}
}

func (e *env) do(t *testing.T, method, target string, body io.Reader, contentType string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, body)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func (e *env) postForm(t *testing.T, target string, v url.Values) *httptest.ResponseRecorder {
	return e.do(t, http.MethodPost, target, strings.NewReader(v.Encode()), "application/x-www-form-urlencoded")
}

func validSearch() url.Values {
	return url.Values{"topic": {"Climate change and agriculture"}, "style": {"apa"}, "count": {"10"}}
}

func TestPagesRender(t *testing.T) {
	e := newEnv(t, "")
	for _, path := range []string{"/", "/references/search", "/references/upload", "/references/saved"} {
		rec := e.do(t, http.MethodGet, path, nil, "")
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "CiteWise") {
			t.Errorf("%s: status %d", path, rec.Code)
		}
		if rec.Header().Get("Content-Security-Policy") == "" {
			t.Errorf("%s: missing CSP header", path)
		}
	}
	// Visitors are told to ask the host; host-only setup details are not shown.
	for _, path := range []string{"/", "/references/search", "/references/upload"} {
		body := e.do(t, http.MethodGet, path, nil, "").Body.String()
		if strings.Contains(body, "OPENAI_API_KEY") || strings.Contains(body, ".env") {
			t.Errorf("%s shows host-only configuration details to visitors", path)
		}
	}
	if body := e.do(t, http.MethodGet, "/references/upload", nil, "").Body.String(); !strings.Contains(body, "host to add an OpenAI API key") {
		t.Error("upload page should tell visitors to ask the host for an API key")
	}
	if rec := e.do(t, http.MethodGet, "/nope", nil, ""); rec.Code != http.StatusNotFound {
		t.Errorf("unknown path: %d", rec.Code)
	}
	if rec := e.do(t, http.MethodGet, "/static/style.css", nil, ""); rec.Code != http.StatusOK {
		t.Errorf("static css: %d", rec.Code)
	}
}

func TestHealth(t *testing.T) {
	e := newEnv(t, "")
	rec := e.do(t, http.MethodGet, "/health", nil, "")
	var body map[string]any
	json.Unmarshal(rec.Body.Bytes(), &body)
	if rec.Code != 200 || body["status"] != "ok" || body["database"] != "ok" || body["ai_enabled"] != false {
		t.Fatalf("health: %d %v", rec.Code, body)
	}
	e.store.Close()
	if rec := e.do(t, http.MethodGet, "/health", nil, ""); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("health with closed DB: %d", rec.Code)
	}
}

func TestSearchValidation(t *testing.T) {
	e := newEnv(t, "")
	cases := map[string]url.Values{
		"Enter an essay title":         {"topic": {""}, "style": {"apa"}, "count": {"10"}},
		"Choose APA 7 or Harvard":      {"topic": {"Valid topic"}, "style": {"mla"}, "count": {"10"}},
		"Choose between 1 and 50":      {"topic": {"Valid topic"}, "style": {"apa"}, "count": {"500"}},
		"start year as a number":       {"topic": {"Valid topic"}, "style": {"apa"}, "count": {"5"}, "year_from": {"abc"}},
		"must not be before the start": {"topic": {"Valid topic"}, "style": {"apa"}, "count": {"5"}, "year_from": {"2020"}, "year_to": {"2010"}},
	}
	for want, form := range cases {
		rec := e.postForm(t, "/references/search", form)
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("%q: status %d", want, rec.Code)
		}
	}
	// Submitted values are preserved and escaped.
	rec := e.postForm(t, "/references/search", url.Values{"topic": {`<script>x</script>`}, "style": {"apa"}, "count": {"0"}})
	if strings.Contains(rec.Body.String(), "<script>x</script>") {
		t.Error("topic not escaped")
	}
}

func TestSearchFlowAndStyles(t *testing.T) {
	e := newEnv(t, "")
	rec := e.postForm(t, "/references/search", validSearch())
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, "/references/results/") {
		t.Fatalf("location %q", loc)
	}
	if e.search.got.Count != 10 || e.search.got.Topic != "Climate change and agriculture" {
		t.Fatalf("request passed to searcher: %+v", e.search.got)
	}

	page := e.do(t, http.MethodGet, loc, nil, "").Body.String()
	for _, want := range []string{
		"Lobell, D. (2018). Climate change and crop yields. <em>Nature Climate Change</em>, <em>8</em>, 1–5. https://doi.org/10.1/crop",
		"Only 2 relevant publications were found",
		"OpenAlex could not be reached",
		"Adaptation without DOI. (2010).",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("results page missing %q", want)
		}
	}
	harvard := e.do(t, http.MethodGet, loc+"?style=harvard", nil, "").Body.String()
	if !strings.Contains(harvard, "Lobell, D. (2018) ‘Climate change and crop yields’") {
		t.Error("style switch to Harvard did not reformat citations")
	}
	if rec := e.do(t, http.MethodGet, "/references/results/999", nil, ""); rec.Code != http.StatusNotFound {
		t.Errorf("missing results: %d", rec.Code)
	}
	if rec := e.do(t, http.MethodGet, "/references/results/abc", nil, ""); rec.Code != http.StatusNotFound {
		t.Errorf("bad id: %d", rec.Code)
	}
}

func TestSearchNoResultsAndProviderFailure(t *testing.T) {
	e := newEnv(t, "")
	e.search.res = search.Result{Queries: []string{"x"}}
	rec := e.postForm(t, "/references/search", validSearch())
	page := e.do(t, http.MethodGet, rec.Header().Get("Location"), nil, "").Body.String()
	if !strings.Contains(page, "No suitable publications were found") {
		t.Error("expected honest empty-results message")
	}
	e.search.err = search.ErrProvidersUnavailable
	rec = e.postForm(t, "/references/search", validSearch())
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "could not be reached") {
		t.Fatalf("provider failure: %d", rec.Code)
	}
}

func firstPublicationID(t *testing.T, e *env) int64 {
	t.Helper()
	searches, _ := e.store.ListSearches(context.Background(), 1)
	rows, _ := e.store.ListResults(context.Background(), searches[0].ID)
	return rows[0].Publication.ID
}

func TestSaveRemoveAndExport(t *testing.T) {
	e := newEnv(t, "")
	e.postForm(t, "/references/search", validSearch())
	pubID := firstPublicationID(t, e)

	rec := e.postForm(t, "/references/save", url.Values{"action": {"save"}, "publication_id": {itoa(pubID)}, "return_to": {"https://evil.example/"}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/references/saved" {
		t.Fatalf("save: %d location=%q", rec.Code, rec.Header().Get("Location"))
	}
	saved := e.do(t, http.MethodGet, "/references/saved", nil, "").Body.String()
	if !strings.Contains(saved, "Climate change and crop yields") || !strings.Contains(saved, "Remove from saved") {
		t.Fatal("saved page does not list the reference")
	}

	for format, want := range map[string]string{"txt": "References (Harvard)", "bibtex": "@article{lobell2018climate,"} {
		rec = e.postForm(t, "/references/export", url.Values{"publication_id": {itoa(pubID)}, "format": {format}, "style": {"harvard"}})
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("export %s: %d %s", format, rec.Code, rec.Body.String())
		}
		if !strings.HasPrefix(rec.Header().Get("Content-Disposition"), "attachment;") {
			t.Errorf("export %s: missing attachment header", format)
		}
	}
	if rec = e.postForm(t, "/references/export", url.Values{"format": {"txt"}}); rec.Code != http.StatusBadRequest ||
		!strings.Contains(rec.Body.String(), "Select at least one") {
		t.Errorf("empty export: %d", rec.Code)
	}
	if rec = e.postForm(t, "/references/export", url.Values{"publication_id": {itoa(pubID)}, "format": {"docx"}}); rec.Code != http.StatusBadRequest {
		t.Errorf("bad format: %d", rec.Code)
	}

	rec = e.postForm(t, "/references/save", url.Values{"action": {"remove"}, "publication_id": {itoa(pubID)}, "return_to": {"/references/saved?style=apa"}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/references/saved?style=apa" {
		t.Fatalf("remove: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if list, _ := e.store.ListSaved(context.Background()); len(list) != 0 {
		t.Fatal("reference not removed")
	}

	for name, form := range map[string]url.Values{
		"bad id":         {"action": {"save"}, "publication_id": {"x"}},
		"unknown action": {"action": {"delete-all"}, "publication_id": {"1"}},
	} {
		if rec := e.postForm(t, "/references/save", form); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", name, rec.Code)
		}
	}
	if rec := e.postForm(t, "/references/save", url.Values{"action": {"save"}, "publication_id": {"99999"}}); rec.Code != http.StatusNotFound {
		t.Errorf("unknown publication: %d", rec.Code)
	}
}

func TestCrossOriginPostRejected(t *testing.T) {
	e := newEnv(t, "")
	req := httptest.NewRequest(http.MethodPost, "/references/save", strings.NewReader("action=save&publication_id=1"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-origin POST: %d", rec.Code)
	}
}

func multipartBody(t *testing.T, filename string, content []byte, fields map[string]string) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		mw.WriteField(k, v)
	}
	if filename != "" {
		fw, _ := mw.CreateFormFile("essay", filename)
		fw.Write(content)
	}
	mw.Close()
	return &buf, mw.FormDataContentType()
}

const essayText = "Rising temperatures are reducing maize yields across sub-Saharan Africa, threatening food security. " +
	"Farmers increasingly rely on irrigation to adapt. SECRET-ESSAY-MARKER. " +
	"These changes are expected to intensify over the coming decades as warming continues."

func TestUploadRequiresAI(t *testing.T) {
	e := newEnv(t, "")
	body, ct := multipartBody(t, "essay.txt", []byte(essayText), map[string]string{"style": "apa"})
	rec := e.do(t, http.MethodPost, "/references/upload", body, ct)
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "isn&#39;t enabled on this server") {
		t.Fatalf("upload without AI: %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "OPENAI_API_KEY") {
		t.Fatal("visitor-facing error exposes host configuration details")
	}
}

func TestUploadFlow(t *testing.T) {
	e := newEnv(t, "test-key")
	e.essays.rep = essay.Report{
		Topic:   "Climate change and African agriculture",
		Queries: []string{"maize yields warming"},
		Claims: []essay.ClaimResult{
			{Index: 0, Section: 1, Claim: "Warming reduces maize yields.", Query: "maize yields warming",
				Matches: []essay.Match{{Publication: searchResult().Items[0].Publication, Score: 2, Rationale: "Measures yield losses from warming."}}},
			{Index: 1, Section: 1, Claim: "Irrigation helps adaptation.", Query: "irrigation adaptation"},
		},
	}
	long := strings.Repeat(essayText+"\n\n", 3)
	body, ct := multipartBody(t, `..\..\evil\essay.txt`, []byte(long), map[string]string{"style": "harvard", "year_from": "2000"})
	rec := e.do(t, http.MethodPost, "/references/upload", body, ct)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(e.essays.gotText, "SECRET-ESSAY-MARKER") {
		t.Fatal("extracted text was not passed to the analyzer")
	}
	page := e.do(t, http.MethodGet, rec.Header().Get("Location"), nil, "").Body.String()
	for _, want := range []string{"Warming reduces maize yields.", "Measures yield losses from warming.", "Approve &amp; save",
		"No suitable publication was found for this claim", "essay.txt", "Lobell, D. (2018) ‘Climate change and crop yields’"} {
		if !strings.Contains(page, want) {
			t.Errorf("results page missing %q", want)
		}
	}
	if strings.Contains(page, "evil") {
		t.Error("uploaded filename path was not sanitised")
	}

	// Temporary files are removed and the essay text is not persisted.
	if entries, _ := os.ReadDir(e.tempDir); len(entries) != 0 {
		t.Fatalf("temporary upload files left behind: %d", len(entries))
	}
	assertNotInDB(t, e.dbPath, "SECRET-ESSAY-MARKER")

	// Approve the suggestion: it becomes a saved reference.
	searches, _ := e.store.ListSearches(context.Background(), 1)
	rows, _ := e.store.ListResults(context.Background(), searches[0].ID)
	rec = e.postForm(t, "/references/save", url.Values{"action": {"approve"}, "result_id": {itoa(rows[0].ID)}, "return_to": {"/references/results/1"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("approve: %d", rec.Code)
	}
	rows, _ = e.store.ListResults(context.Background(), searches[0].ID)
	if rows[0].Approved != 1 || !rows[0].Saved {
		t.Fatalf("approval not applied: %+v", rows[0])
	}
	rec = e.postForm(t, "/references/save", url.Values{"action": {"reject"}, "result_id": {itoa(rows[0].ID)}})
	rows, _ = e.store.ListResults(context.Background(), searches[0].ID)
	if rows[0].Approved != -1 || rows[0].Saved {
		t.Fatalf("rejection not applied: %+v", rows[0])
	}
	doc, err := e.store.DocumentForSearch(context.Background(), searches[0].ID)
	if err != nil || doc.Status != "processed" {
		t.Fatalf("document: %v %+v", err, doc)
	}
}

func TestUploadErrors(t *testing.T) {
	e := newEnv(t, "test-key")
	cases := []struct {
		name     string
		filename string
		content  []byte
		status   int
		want     string
	}{
		{"no file", "", nil, http.StatusUnprocessableEntity, "Choose a PDF, DOCX or TXT file"},
		{"bad type", "essay.exe", []byte("MZ..."), http.StatusUnprocessableEntity, "Unsupported file type"},
		{"fake pdf", "essay.pdf", []byte(essayText), http.StatusUnprocessableEntity, "valid PDF"},
		{"too large", "essay.txt", bytes.Repeat([]byte("a"), 3<<20), http.StatusRequestEntityTooLarge, "larger than the 1 MB limit"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body, ct := multipartBody(t, c.filename, c.content, map[string]string{"style": "apa"})
			rec := e.do(t, http.MethodPost, "/references/upload", body, ct)
			if rec.Code != c.status || !strings.Contains(rec.Body.String(), c.want) {
				t.Fatalf("status %d, body missing %q", rec.Code, c.want)
			}
		})
	}
	e.essays.err = errors.Join(ai.ErrUnavailable, errors.New("OpenAI rejected the API key (HTTP 401)"))
	body, ct := multipartBody(t, "essay.txt", []byte(essayText), map[string]string{"style": "apa"})
	rec := e.do(t, http.MethodPost, "/references/upload", body, ct)
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "let the site&#39;s host know") {
		t.Fatalf("AI failure: %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "test-key") || strings.Contains(rec.Body.String(), "HTTP 401") {
		t.Fatal("AI failure details leaked to the visitor")
	}
	if entries, _ := os.ReadDir(e.tempDir); len(entries) != 0 {
		t.Fatalf("temporary files left behind after errors: %d", len(entries))
	}
}

func TestSafeReturn(t *testing.T) {
	cases := map[string]string{
		"/references/saved":     "/references/saved",
		"https://evil.example/": "/fallback",
		"//evil.example":        "/fallback",
		`/\evil.example`:        "/fallback",
		"":                      "/fallback",
		"relative":              "/fallback",
	}
	for in, want := range cases {
		if got := safeReturn(in, "/fallback"); got != want {
			t.Errorf("safeReturn(%q) = %q, want %q", in, got, want)
		}
	}
}

func assertNotInDB(t *testing.T, path, needle string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{
		"SELECT COUNT(*) FROM searches WHERE topic || description || claims_json || notice LIKE ?",
		"SELECT COUNT(*) FROM search_results WHERE claim || rationale LIKE ?",
		"SELECT COUNT(*) FROM documents WHERE filename || error LIKE ?",
	} {
		var n int
		if err := db.QueryRow(q, "%"+needle+"%").Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatalf("essay text found in database (%s)", q)
		}
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

type fakeDOI struct {
	pub models.Publication
	err error
	got string
}

func (f *fakeDOI) LookupDOI(_ context.Context, doi string) (models.Publication, error) {
	f.got = doi
	return f.pub, f.err
}

func TestAddByDOI(t *testing.T) {
	e := newEnv(t, "")
	e.doi.pub = models.Publication{DOI: "10.1080/09638237.2020.1714007", Title: "Social media and adolescent mental health",
		Authors: []models.Author{{Given: "Michelle", Family: "O'Reilly"}}, Year: 2020, Venue: "Journal of Mental Health",
		Volume: "29", Issue: "2", Pages: "200-206", Type: "journal-article", Source: "crossref"}

	rec := e.postForm(t, "/references/doi", url.Values{"doi": {" https://doi.org/10.1080/09638237.2020.1714007 "}, "style": {"harvard"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("add: %d %s", rec.Code, rec.Body.String())
	}
	if e.doi.got != "10.1080/09638237.2020.1714007" {
		t.Fatalf("resolver got %q, want normalized DOI", e.doi.got)
	}
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, "/references/saved?style=harvard&added=") || strings.Contains(loc, "already") {
		t.Fatalf("location %q", loc)
	}
	saved, _ := e.store.ListSaved(context.Background())
	if id := itoa(saved[0].Publication.ID); !strings.HasSuffix(loc, "added="+id+"#p"+id) {
		t.Errorf("location %q should identify and anchor to the added reference", loc)
	}
	page := e.do(t, http.MethodGet, browserURL(loc), nil, "").Body.String()
	for _, want := range []string{"Added to your saved references", "O&#39;Reilly, M. (2020) ‘Social media and adolescent mental health’", "pp. 200–206"} {
		if !strings.Contains(page, want) {
			t.Errorf("saved page missing %q", want)
		}
	}

	// Adding the same DOI again does not duplicate it.
	rec = e.postForm(t, "/references/doi", url.Values{"doi": {"10.1080/09638237.2020.1714007"}, "style": {"apa"}})
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "already=1") {
		t.Fatalf("second add location %q", loc)
	}
	page = e.do(t, http.MethodGet, browserURL(rec.Header().Get("Location")), nil, "").Body.String()
	if !strings.Contains(page, "already in your saved references") {
		t.Error("expected already-saved notice")
	}
	if saved, _ := e.store.ListSaved(context.Background()); len(saved) != 1 {
		t.Fatalf("expected 1 saved reference, got %d", len(saved))
	}
}

func TestAddByDOIErrors(t *testing.T) {
	e := newEnv(t, "")
	cases := []struct {
		name   string
		input  string
		err    error
		status int
		want   string
	}{
		{"empty", "", nil, http.StatusUnprocessableEntity, "Enter a DOI"},
		{"not a doi", "the great gatsby", nil, http.StatusUnprocessableEntity, "look like a DOI"},
		{"unknown", "10.1000/missing", search.ErrDOINotFound, http.StatusNotFound, "No publication with the DOI 10.1000/missing"},
		{"providers down", "10.1000/abc", search.ErrProvidersUnavailable, http.StatusBadGateway, "could not be reached"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e.doi.err = c.err
			rec := e.postForm(t, "/references/doi", url.Values{"doi": {c.input}, "style": {"apa"}})
			body := rec.Body.String()
			if rec.Code != c.status || !strings.Contains(body, c.want) {
				t.Fatalf("status %d, body missing %q", rec.Code, c.want)
			}
			if c.input != "" && !strings.Contains(body, `value="`+c.input+`"`) {
				t.Error("entered DOI should be kept in the form")
			}
		})
	}
	if saved, _ := e.store.ListSaved(context.Background()); len(saved) != 0 {
		t.Fatal("failed lookups must not save anything")
	}
}

// browserURL drops the #fragment, which browsers never send to the server.
func browserURL(loc string) string {
	if i := strings.Index(loc, "#"); i >= 0 {
		return loc[:i]
	}
	return loc
}
