# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project status

CiteWise AI is an academic reference finder for students and teachers. At the time of writing, the repo holds only `PROMPT.MD`, the full requirements. It has no code yet and is not a git repository. `PROMPT.MD` refers to a `CITEWISE_SPEC.md` that does not exist. Treat `PROMPT.MD` as the spec, and read it in full before you start a milestone. Update this file once real code and commands exist.

## Hard constraints (from PROMPT.MD)

- **Stack:** Go, `net/http` for routing, and `html/template` for all HTML. The frontend uses plain CSS and minimal vanilla JS. SQLite is the only database; use a pure-Go driver such as `modernc.org/sqlite`. The whole app runs as one Go server.
- **Forbidden:** React, Node.js, npm, any frontend build step, and any database other than SQLite.
- **No fabricated bibliographic data.** Titles, authors, years, venues, and DOIs must come from Crossref or OpenAlex records. OpenAI may only generate queries, analyze essays, and assess relevance. Never pad results to reach the requested count. If nothing suitable is found, show an honest message.
- **Citation styles:** APA 7 and Harvard only. Format citations from stored metadata so users can switch styles without searching again. When metadata is missing, leave it out instead of inventing it.
- **AI is optional.** Title search must work without `OPENAI_API_KEY`, using a keyword-based query fallback. Essay analysis needs the key, and the UI must say so.
- **Uploaded essays are untrusted input.** Guard against prompt injection in AI calls. Process uploads in temp files and always clean them up. Never persist essay files or extracted text. Accepted formats are PDF, DOCX, and TXT, with type and size validated. No OCR.
- **Outbound calls:** use timeouts, context cancellation, and basic retries. Errors shown to users must not expose credentials or stack traces.
- **No auth.** Saved references are global to the local DB, and the README must say so.

## Intended architecture

- **Provider clients:** separate Crossref and OpenAlex components. Both normalize into one publication model.
- **Search pipeline:** generate queries (AI or keyword fallback), fan out to both providers, normalize, deduplicate (by DOI first, with a fuzzy title/author/year fallback), rank by keyword relevance, then apply filters (year range, count).
- **Essay pipeline:** upload, extract text, have AI pick out the topic and claims per section, run targeted searches per claim, then match publications to passages. The user reviews and approves the suggestions.
- **Persistence:** SQLite at `DATABASE_PATH` (default `./data/citewise.db`), initialized on startup. Tables: `searches`, `documents`, `publications`, `search_results`, `saved_references`. Use foreign keys, indexes, and parameterized SQL only.
- **Exports:** TXT and BibTeX for selected references.
- **Routes** (Go 1.22+ method/path patterns): `GET /`, `GET|POST /references/search`, `GET|POST /references/upload`, `GET /references/results/{id}`, `GET /references/saved`, `POST /references/save` (save or remove), `POST /references/export`, `GET /health`.

## Configuration

Commit `.env.example` with these keys: `OPENAI_API_KEY`, `OPENAI_MODEL`, `PORT=8080`, `DATABASE_PATH=./data/citewise.db`, `MAX_UPLOAD_MB=10`, `TEMP_DIR`, `CROSSREF_MAILTO`, `OPENALEX_API_KEY`. Never commit `.env`, `data/`, or uploaded files.

## Commands

```bash
go run .                      # or ./cmd/... depending on final layout
go test ./...
go test ./internal/<pkg> -run TestName   # single test
```

Run all of these before declaring work done, and report the actual output:

```bash
gofmt -w . && go mod tidy && go vet ./... && go test ./... && go build ./...
```

Tests must use a temporary SQLite DB and mocked HTTP responses (`httptest`). They must never need real API keys or make paid AI calls.

## Git workflow

Commit at each verifiable milestone (the suggested list is in PROMPT.MD §9), using conventional-commit prefixes (`chore:`, `feat:`, `test:`). Before each commit: run gofmt, run the relevant tests, inspect the diff, and stage only that milestone's files. Never amend commits and never push without permission. The repo is not initialized yet, so run `git init` first.
