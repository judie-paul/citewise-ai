# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project

CiteWise AI is a single Go web server that finds real academic references (Crossref and OpenAlex) for an essay title or an uploaded essay, and formats them as APA 7 or Harvard. The full requirements are in `CITEWISE_SPEC.MD`, and the user-facing docs are in `README.md`.

**Layout:** the Go module (`module citewise`, Go 1.26+) lives in `code/`. Executables go in `build/`; the folder is kept in Git through `.gitkeep`, but the binaries are git-ignored. Run Go commands from `code/`, or use `go -C code ...`.

## Commands

```bash
build.bat [test]               # Windows cmd; or .\build.ps1 [-Test], ./build.sh [--test]
cd code && go run .            # dev run (reads code/.env, DB at code/data/)
cd code && go test ./...
go test ./internal/web -run TestUploadFlow      # single test
```

Run this before declaring work done, and report the real output:

```bash
cd code && gofmt -l . && go mod tidy && go vet ./... && go test ./... && go build ./...
```

`go test -race` is unavailable on this machine because CGO is off. Tests must use temp SQLite DBs and `httptest` mocks; never require real API keys.

## Architecture

The flow is `main.go` → `web.Server`. The server depends on two interfaces so handlers can be tested with fakes: `web.Searcher` (implemented by `search.Service`) and `web.EssayAnalyzer` (implemented by `essay.Service`). `Essays` is nil when `OPENAI_API_KEY` is unset.

- **Title search** (`internal/search`): build queries (AI via the `QueryGenerator` interface, or the `FallbackQueries` keyword fallback), then `Gather` fans out to every `providers.Provider` concurrently. Next come `Dedupe` (DOI first, then normalized title with a compatible first author and year ±1; Crossref fields win on merge), `FilterYears`, and `Rank` (keyword overlap; drops zero-overlap items). If every provider fails, `Find` returns `ErrProvidersUnavailable`. Partial failures become user-visible warnings.
- **Essay pipeline** (`internal/essay`): `Split` divides text into sections (at most 12 × 2,500 chars). `ai.AnalyzeEssay` returns the topic and claims. Each claim gets a `Gather`/`Rank` search (3 in parallel). Then `ai.AssessRelevance` picks candidates **by app-assigned integer ID only**, and IDs not offered for that claim are discarded. If the relevance check fails, the top two keyword matches are used instead.
- **Cite by DOI** (`POST /references/doi`): `models.ParseDOI` accepts bare, `doi:` and `doi.org` forms. `search.Service.LookupDOI` tries each provider that implements `providers.DOILookuper` (Crossref, then OpenAlex) and **rejects records whose DOI differs from the one requested**, because OpenAlex merges versions of a work. `store.AddReference` upserts the record and saves it in one transaction.
- **AI** (`internal/ai`): uses raw `net/http` to call Chat Completions in JSON mode. It may only generate queries, analyse essays, and assess relevance; it must never produce bibliographic data. User text is wrapped in tag delimiters, and `fence()` neutralises those tags inside the text. All AI output is clipped and validated.
- **Providers** (`internal/providers/{crossref,openalex}`): each normalizes into `models.Publication`. `httpx.Client` handles retries (429/5xx), body limits, and **strips URLs from errors** because the OpenAlex URL carries `api_key`.
- **Citations** (`internal/citation`): `Format` returns segments (plain or italic) rendered by `.Text()` or `.HTML()` (escaped). Style is chosen at render time (`?style=`), so persisted results never store formatted citations.
- **Store** (`internal/store`): the schema lives in a `const` and is applied with `CREATE TABLE IF NOT EXISTS` on startup. There is no migration system, so new columns need a migration once real databases exist. Publications are upserted on `DedupKey`. `search_results.claim_index` is -1 for title searches. `searches.claims_json` keeps the AI's claim paraphrases so claims with no matches still show up.
- **Web** (`internal/web`): Go 1.22+ `ServeMux` method patterns. Templates are embedded: each page is parsed together with `layout.html`, which also defines the shared partials `ref`, `export-bar` and `style-switch`. Export checkboxes use `form="export-form"` to avoid nested forms. The CSP forbids inline scripts and styles (no `style=` attributes or inline `<script>`). POSTs are checked for same origin, and redirects go through `safeReturn`.

## Invariants (from the spec)

- Never fabricate publications, authors, DOIs or years, and never pad results to reach the requested count. Show an honest empty or short-result message instead.
- Do not persist essay files or extracted text. Uploads go through `extract.Process`, which always deletes its temp file.
- Allowed stack: Go stdlib `net/http` + `html/template`, plain CSS, minimal vanilla JS, SQLite (pure Go). No Node, npm, frontend frameworks, or other databases.
- Errors shown to users must not expose keys or stack traces. `extract.UserError` messages are safe to show verbatim.

## Git

Use conventional commits (`feat:`, `chore:`, `test:`). Run gofmt and tests before committing, stage only the files for that milestone, never amend, and never push without permission. `.gitattributes` forces LF line endings, so `gofmt` stays clean on Windows.
