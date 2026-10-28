package web

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"citewise/internal/ai"
	"citewise/internal/citation"
	"citewise/internal/essay"
	"citewise/internal/export"
	"citewise/internal/extract"
	"citewise/internal/models"
	"citewise/internal/search"
	"citewise/internal/store"
)

// Searcher runs title searches.
type Searcher interface {
	Find(ctx context.Context, req search.Request) (search.Result, error)
}

// EssayAnalyzer produces citation suggestions for essay text.
type EssayAnalyzer interface {
	Analyze(ctx context.Context, text string, yearFrom, yearTo int) (essay.Report, error)
}

const (
	maxFormBytes  = 64 << 10
	maxExportRefs = 500
)

var templateFuncs = template.FuncMap{
	"add":   func(a, b int) int { return a + b },
	"lines": func(s string) []string { return strings.FieldsFunc(s, func(r rune) bool { return r == '\n' }) },
	"date":  func(t time.Time) string { return t.Local().Format("2 Jan 2006, 15:04") },
	"join":  strings.Join,
	"styleNav": func(base string, current citation.Style) map[string]string {
		return map[string]string{"Base": base, "Current": string(current)}
	},
	"styleLabel": func(s string) string {
		st, _ := citation.ParseStyle(s)
		return st.Label()
	},
}

// --- title search ---

type searchForm struct {
	Topic       string
	Description string
	Style       string
	Count       string
	YearFrom    string
	YearTo      string
	MaxYear     int
	Errors      map[string]string
	Error       string
}

func (s *Server) handleSearchForm(w http.ResponseWriter, r *http.Request) {
	s.render(w, http.StatusOK, "search.html", "Search by title", "search",
		searchForm{Style: "apa", Count: "10", MaxYear: s.currentYr + 1, Topic: strings.TrimSpace(r.URL.Query().Get("topic"))})
}

// parseYear parses an optional year field; empty means 0.
func parseOptionalInt(v string) (int, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, true
	}
	n, err := strconv.Atoi(v)
	return n, err == nil
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	if err := r.ParseForm(); err != nil {
		s.renderError(w, http.StatusBadRequest, "The form could not be read. Please try again.", "/references/search")
		return
	}
	form := searchForm{
		Topic:       strings.TrimSpace(r.PostForm.Get("topic")),
		Description: strings.TrimSpace(r.PostForm.Get("description")),
		Style:       r.PostForm.Get("style"),
		Count:       strings.TrimSpace(r.PostForm.Get("count")),
		YearFrom:    strings.TrimSpace(r.PostForm.Get("year_from")),
		YearTo:      strings.TrimSpace(r.PostForm.Get("year_to")),
		MaxYear:     s.currentYr + 1,
	}
	req := search.Request{Topic: form.Topic, Description: form.Description}
	errs := map[string]string{}
	var ok bool
	if req.Count, ok = parseOptionalInt(form.Count); !ok || form.Count == "" {
		req.Count = -1
	}
	if req.YearFrom, ok = parseOptionalInt(form.YearFrom); !ok {
		errs["year_from"] = "Enter the start year as a number, e.g. 2015."
	}
	if req.YearTo, ok = parseOptionalInt(form.YearTo); !ok {
		errs["year_to"] = "Enter the end year as a number, e.g. 2024."
	}
	for k, v := range req.Validate(s.currentYr) {
		if errs[k] == "" {
			errs[k] = v
		}
	}
	style, ok := citation.ParseStyle(form.Style)
	if !ok {
		errs["style"] = "Choose APA 7 or Harvard."
	}
	if len(errs) > 0 {
		form.Errors = errs
		s.render(w, http.StatusUnprocessableEntity, "search.html", "Search by title", "search", form)
		return
	}

	res, err := s.searcher.Find(r.Context(), req)
	if err != nil {
		if r.Context().Err() != nil {
			return // client went away
		}
		s.log.Error("title search failed", "err", err)
		form.Error = "The academic databases (Crossref and OpenAlex) could not be reached. Check your internet connection and try again in a moment."
		s.render(w, http.StatusBadGateway, "search.html", "Search by title", "search", form)
		return
	}

	results := make([]store.ResultInput, 0, len(res.Items))
	for _, it := range res.Items {
		results = append(results, store.ResultInput{Publication: it.Publication, Score: it.Score, ClaimIndex: -1})
	}
	id, err := s.store.SaveSearch(r.Context(), store.Search{
		Topic: req.Topic, Description: req.Description, Type: "title", Style: string(style),
		RequestedCount: req.Count, YearFrom: req.YearFrom, YearTo: req.YearTo,
		AIUsed: res.AIUsed, Queries: res.Queries, Notice: strings.Join(res.Warnings, "\n"),
	}, results)
	if err != nil {
		s.log.Error("save search", "err", err)
		s.renderError(w, http.StatusInternalServerError, "The search results could not be saved to the database.", "/references/search")
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/references/results/%d", id), http.StatusSeeOther)
}

// --- essay upload ---

type uploadForm struct {
	Style    string
	YearFrom string
	YearTo   string
	MaxMB    int
	MaxYear  int
	Error    string
	Errors   map[string]string
}

func (s *Server) newUploadForm() uploadForm {
	return uploadForm{Style: "apa", MaxMB: s.cfg.MaxUploadMB, MaxYear: s.currentYr + 1}
}

func (s *Server) handleUploadForm(w http.ResponseWriter, r *http.Request) {
	s.render(w, http.StatusOK, "upload.html", "Upload an essay", "upload", s.newUploadForm())
}

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	form := s.newUploadForm()
	fail := func(status int, msg string) {
		form.Error = msg
		s.render(w, status, "upload.html", "Upload an essay", "upload", form)
	}
	if !s.cfg.AIEnabled() || s.essays == nil {
		fail(http.StatusServiceUnavailable, "AI-assisted essay analysis requires an OpenAI API key. Ask the administrator to set OPENAI_API_KEY, or use title search, which works without one.")
		return
	}

	maxBytes := s.cfg.MaxUploadBytes()
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes+(1<<20))
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			fail(http.StatusRequestEntityTooLarge, fmt.Sprintf("The file is larger than the %d MB limit.", s.cfg.MaxUploadMB))
			return
		}
		fail(http.StatusBadRequest, "The upload could not be read. Please choose a file and try again.")
		return
	}
	defer r.MultipartForm.RemoveAll()

	form.Style = r.FormValue("style")
	form.YearFrom = strings.TrimSpace(r.FormValue("year_from"))
	form.YearTo = strings.TrimSpace(r.FormValue("year_to"))
	errs := map[string]string{}
	style, ok := citation.ParseStyle(form.Style)
	if !ok {
		errs["style"] = "Choose APA 7 or Harvard."
	}
	yearFrom, ok1 := parseOptionalInt(form.YearFrom)
	yearTo, ok2 := parseOptionalInt(form.YearTo)
	if !ok1 {
		errs["year_from"] = "Enter the start year as a number, e.g. 2015."
	}
	if !ok2 {
		errs["year_to"] = "Enter the end year as a number, e.g. 2024."
	}
	if ok1 && ok2 {
		// Reuse the title-search year validation.
		v := search.Request{Topic: "placeholder topic", Count: 1, YearFrom: yearFrom, YearTo: yearTo}.Validate(s.currentYr)
		for _, k := range []string{"year_from", "year_to"} {
			if v[k] != "" {
				errs[k] = v[k]
			}
		}
	}
	file, header, err := r.FormFile("essay")
	if err != nil {
		errs["essay"] = "Choose a PDF, DOCX or TXT file to upload."
	} else {
		defer file.Close()
	}
	if len(errs) > 0 {
		form.Errors = errs
		s.render(w, http.StatusUnprocessableEntity, "upload.html", "Upload an essay", "upload", form)
		return
	}

	filename := clipName(filepath.Base(strings.ReplaceAll(header.Filename, `\`, "/")))
	docID, err := s.store.CreateDocument(r.Context(), filename, header.Size)
	if err != nil {
		s.log.Error("create document", "err", err)
		fail(http.StatusInternalServerError, "The upload could not be recorded in the database.")
		return
	}
	markFailed := func(msg string) {
		if err := s.store.UpdateDocument(context.Background(), docID, "failed", 0, msg); err != nil {
			s.log.Error("update document", "err", err)
		}
	}

	extracted, err := extract.Process(s.cfg.TempDir, filename, file, maxBytes)
	if err != nil {
		msg, ok := extract.IsUserError(err)
		if !ok {
			s.log.Error("extract document", "err", err)
			msg = "The document could not be processed."
		}
		markFailed(msg)
		fail(http.StatusUnprocessableEntity, msg)
		return
	}

	report, err := s.essays.Analyze(r.Context(), extracted.Text, yearFrom, yearTo)
	extracted.Text = "" // the essay text is not kept beyond analysis
	if err != nil {
		if r.Context().Err() != nil {
			markFailed("cancelled")
			return
		}
		s.log.Error("essay analysis failed", "err", err)
		var msg string
		switch {
		case errors.Is(err, essay.ErrAINotConfigured):
			msg = essay.ErrAINotConfigured.Error() + "."
		case errors.Is(err, ai.ErrUnavailable):
			msg = "AI essay analysis failed: " + err.Error() + ". Title search still works without AI."
		case errors.Is(err, search.ErrProvidersUnavailable):
			msg = "The academic databases (Crossref and OpenAlex) could not be reached. Try again in a moment."
		default:
			msg = "The essay could not be analysed. Please try again."
		}
		markFailed(msg)
		fail(http.StatusBadGateway, msg)
		return
	}

	warnings := report.Warnings
	if extracted.Truncated {
		warnings = append([]string{fmt.Sprintf("Only the first %d characters of the essay were used.", extract.MaxTextChars)}, warnings...)
	}
	srch := store.Search{
		Topic: report.Topic, Description: "Uploaded essay: " + filename, Type: "essay", Style: string(style),
		YearFrom: yearFrom, YearTo: yearTo, AIUsed: true, Queries: report.Queries, Notice: strings.Join(warnings, "\n"),
	}
	var results []store.ResultInput
	for _, c := range report.Claims {
		srch.Claims = append(srch.Claims, store.Claim{Index: c.Index, Section: c.Section, Text: c.Claim})
		for _, m := range c.Matches {
			results = append(results, store.ResultInput{
				Publication: m.Publication, Score: m.Score, ClaimIndex: c.Index, Claim: c.Claim,
				Section: strconv.Itoa(c.Section), Rationale: m.Rationale,
			})
		}
	}
	srch.RequestedCount = len(results)
	id, err := s.store.SaveSearch(r.Context(), srch, results)
	if err != nil {
		s.log.Error("save essay search", "err", err)
		markFailed("results could not be saved")
		fail(http.StatusInternalServerError, "The suggestions could not be saved to the database.")
		return
	}
	if err := s.store.UpdateDocument(r.Context(), docID, "processed", id, ""); err != nil {
		s.log.Error("update document", "err", err)
	}
	http.Redirect(w, r, fmt.Sprintf("/references/results/%d", id), http.StatusSeeOther)
}

func clipName(name string) string {
	if name == "." || name == "/" || name == "" {
		return "upload"
	}
	if r := []rune(name); len(r) > 200 {
		return string(r[:200])
	}
	return name
}

// --- results and saved references ---

// refItem is a publication prepared for display.
type refItem struct {
	Key          string
	ResultID     int64
	Pub          models.Publication
	CitationHTML template.HTML
	CitationText string
	InText       string
	Saved        bool
	Approved     int
	Rationale    string
	ReturnTo     string
	Essay        bool
}

func newRefItem(p models.Publication, style citation.Style, returnTo string) refItem {
	c := citation.Format(p, style)
	return refItem{
		Key: fmt.Sprintf("p%d", p.ID), Pub: p, CitationHTML: c.HTML(), CitationText: c.Text(),
		InText: citation.InText(p, style), ReturnTo: returnTo,
	}
}

type claimGroup struct {
	Index   int
	Section int
	Claim   string
	Items   []refItem
}

type resultsPage struct {
	Search   store.Search
	Style    citation.Style
	Items    []refItem
	Claims   []claimGroup
	Document *store.Document
	ReturnTo string
	Found    int
	BasePath string
}

func (s *Server) styleFromQuery(r *http.Request, fallback string) citation.Style {
	if st, ok := citation.ParseStyle(r.URL.Query().Get("style")); ok {
		return st
	}
	st, ok := citation.ParseStyle(fallback)
	if !ok {
		return citation.APA
	}
	return st
}

func (s *Server) handleResults(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		s.renderError(w, http.StatusNotFound, "That search does not exist.", "/")
		return
	}
	srch, err := s.store.GetSearch(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		s.renderError(w, http.StatusNotFound, "That search does not exist. It may have been removed from the database.", "/")
		return
	}
	if err != nil {
		s.log.Error("get search", "err", err)
		s.renderError(w, http.StatusInternalServerError, "The search could not be loaded.", "/")
		return
	}
	rows, err := s.store.ListResults(r.Context(), id)
	if err != nil {
		s.log.Error("list results", "err", err)
		s.renderError(w, http.StatusInternalServerError, "The search results could not be loaded.", "/")
		return
	}
	style := s.styleFromQuery(r, srch.Style)
	base := fmt.Sprintf("/references/results/%d", id)
	page := resultsPage{Search: srch, Style: style, BasePath: base, ReturnTo: base + "?style=" + string(style), Found: len(rows)}

	toItem := func(row store.Result) refItem {
		it := newRefItem(row.Publication, style, page.ReturnTo)
		it.Key = fmt.Sprintf("r%d", row.ID)
		it.ResultID, it.Saved, it.Approved, it.Rationale = row.ID, row.Saved, row.Approved, row.Rationale
		it.Essay = srch.Type == "essay"
		return it
	}
	if srch.Type == "essay" {
		if doc, err := s.store.DocumentForSearch(r.Context(), id); err == nil {
			page.Document = &doc
		}
		groups := map[int]*claimGroup{}
		for _, c := range srch.Claims {
			page.Claims = append(page.Claims, claimGroup{Index: c.Index, Section: c.Section, Claim: c.Text})
		}
		for i := range page.Claims {
			groups[page.Claims[i].Index] = &page.Claims[i]
		}
		for _, row := range rows {
			g := groups[row.ClaimIndex]
			if g == nil {
				page.Claims = append(page.Claims, claimGroup{Index: row.ClaimIndex, Claim: row.Claim})
				g = &page.Claims[len(page.Claims)-1]
				groups[row.ClaimIndex] = g
			}
			g.Items = append(g.Items, toItem(row))
		}
	} else {
		for _, row := range rows {
			page.Items = append(page.Items, toItem(row))
		}
	}
	title := "Results: " + srch.Topic
	s.render(w, http.StatusOK, "results.html", title, "", page)
}

type savedPage struct {
	Style    citation.Style
	Items    []refItem
	Searches []store.Search
	ReturnTo string
}

func (s *Server) handleSaved(w http.ResponseWriter, r *http.Request) {
	saved, err := s.store.ListSaved(r.Context())
	if err != nil {
		s.log.Error("list saved", "err", err)
		s.renderError(w, http.StatusInternalServerError, "Saved references could not be loaded.", "/")
		return
	}
	searches, err := s.store.ListSearches(r.Context(), 50)
	if err != nil {
		s.log.Error("list searches", "err", err)
	}
	style := s.styleFromQuery(r, "apa")
	page := savedPage{Style: style, Searches: searches, ReturnTo: "/references/saved?style=" + string(style)}
	for _, sr := range saved {
		it := newRefItem(sr.Publication, style, page.ReturnTo)
		it.Saved = true
		page.Items = append(page.Items, it)
	}
	s.render(w, http.StatusOK, "saved.html", "Saved references", "saved", page)
}

func (s *Server) handleSave(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	if err := r.ParseForm(); err != nil {
		s.renderError(w, http.StatusBadRequest, "The form could not be read.", "/references/saved")
		return
	}
	back := safeReturn(r.PostForm.Get("return_to"), "/references/saved")
	action := r.PostForm.Get("action")
	ctx := r.Context()

	var err error
	switch action {
	case "save", "remove":
		pubID, perr := strconv.ParseInt(r.PostForm.Get("publication_id"), 10, 64)
		if perr != nil || pubID <= 0 {
			s.renderError(w, http.StatusBadRequest, "No valid reference was specified.", back)
			return
		}
		if action == "save" {
			err = s.store.SaveReference(ctx, pubID)
		} else {
			err = s.store.RemoveReference(ctx, pubID)
		}
	case "approve", "reject":
		resID, perr := strconv.ParseInt(r.PostForm.Get("result_id"), 10, 64)
		if perr != nil || resID <= 0 {
			s.renderError(w, http.StatusBadRequest, "No valid suggestion was specified.", back)
			return
		}
		pubID, gerr := s.store.ResultPublicationID(ctx, resID)
		if gerr != nil {
			err = gerr
			break
		}
		if action == "approve" {
			if err = s.store.SetApproval(ctx, resID, 1); err == nil {
				err = s.store.SaveReference(ctx, pubID)
			}
		} else {
			if err = s.store.SetApproval(ctx, resID, -1); err == nil {
				err = s.store.RemoveReference(ctx, pubID)
			}
		}
	default:
		s.renderError(w, http.StatusBadRequest, "Unknown action.", back)
		return
	}
	if errors.Is(err, store.ErrNotFound) {
		s.renderError(w, http.StatusNotFound, "That reference no longer exists.", back)
		return
	}
	if err != nil {
		s.log.Error("save action", "action", action, "err", err)
		s.renderError(w, http.StatusInternalServerError, "The change could not be saved.", back)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	if err := r.ParseForm(); err != nil {
		s.renderError(w, http.StatusBadRequest, "The form could not be read.", "/references/saved")
		return
	}
	back := safeReturn(r.PostForm.Get("return_to"), "/references/saved")
	format, ok := export.ParseFormat(r.PostForm.Get("format"))
	if !ok {
		s.renderError(w, http.StatusBadRequest, "Choose an export format: plain text or BibTeX.", back)
		return
	}
	style, ok := citation.ParseStyle(r.PostForm.Get("style"))
	if !ok {
		style = citation.APA
	}
	var ids []int64
	for _, v := range r.PostForm["publication_id"] {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil && id > 0 {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		s.renderError(w, http.StatusBadRequest, "Select at least one reference to export.", back)
		return
	}
	if len(ids) > maxExportRefs {
		s.renderError(w, http.StatusBadRequest, fmt.Sprintf("You can export at most %d references at once.", maxExportRefs), back)
		return
	}
	pubs, err := s.store.GetPublications(r.Context(), ids)
	if err != nil {
		s.log.Error("export load", "err", err)
		s.renderError(w, http.StatusInternalServerError, "The references could not be loaded.", back)
		return
	}
	if len(pubs) == 0 {
		s.renderError(w, http.StatusNotFound, "None of the selected references exist any more.", back)
		return
	}
	f := export.Render(pubs, format, style, time.Now())
	w.Header().Set("Content-Type", f.ContentType)
	w.Header().Set("Content-Disposition", `attachment; filename="`+f.Name+`"`)
	w.Header().Set("Cache-Control", "no-store")
	w.Write(f.Body)
}
