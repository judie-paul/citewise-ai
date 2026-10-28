# CiteWise AI: Academic Reference Finder

CiteWise AI helps students and teachers find **real** academic references for an essay topic or an existing essay. It searches [Crossref](https://www.crossref.org/) and [OpenAlex](https://openalex.org/), removes duplicate records, ranks results by relevance, and formats citations in **APA 7** or **Harvard** style. It never makes up publications, authors, DOIs or years.

---

## Getting API keys (read this first)

> **Who provides the keys?** Whoever **hosts** CiteWise. That's the person or organisation running the server, such as a teacher, school IT department, or a student running it on their own computer. They put the keys in the server's `.env` file once, and every visitor uses them.
>
> **Visitors never need a key.** Students and teachers using the site in a browser aren't asked for one and have nowhere to enter one. If a feature is unavailable because the host hasn't set a key, the site says so. Ask the host to set it up.
>
> **The host pays for usage.** All OpenAI usage is billed to the host's OpenAI account, including every essay any visitor uploads. Set a monthly spending limit in your OpenAI account settings (see step 2 below).

CiteWise **runs without any API keys**. Title search works out of the box. Keys the host adds unlock extra features or raise rate limits:

| Key (set by the host) | Needed for | Required? | Cost to the host |
|---|---|---|---|
| `OPENAI_API_KEY` | AI search queries for title search; **essay upload and analysis** | Only for essay upload | Paid, per use |
| `OPENALEX_API_KEY` | A higher, dedicated OpenAlex request budget | Recommended | Free |
| `CROSSREF_MAILTO` (an email, not a key) | Crossref's faster "polite" API pool | Recommended | Free |

### OpenAI API key (enables essay analysis)

These steps are for the **host**:

1. Go to <https://platform.openai.com/signup> and create an account, or sign in.
2. Open **Settings → Billing** (<https://platform.openai.com/settings/organization/billing>) and add a payment method or credit. API usage is billed separately from a ChatGPT subscription. Under **Limits** (<https://platform.openai.com/settings/organization/limits>), set a monthly budget so visitors' uploads can't run up unexpected costs.
3. Open **API keys** (<https://platform.openai.com/api-keys>) and click **Create new secret key**.
4. Copy the key, which starts with `sk-`. OpenAI shows it only once.
5. Paste it into the `.env` file on the server: `OPENAI_API_KEY=sk-...`
6. Optionally choose a model with `OPENAI_MODEL=` (default: `gpt-4o-mini`, which is inexpensive and works well here).
7. Restart CiteWise. Keys are read only at startup.

Without this key, title search falls back to keyword queries, and the upload page tells visitors that essay analysis isn't available on this server.

### OpenAlex API key (free, recommended)

Without a key, OpenAlex shares a small free daily budget with everyone on the server's network IP address. The host can get a dedicated key:

1. Go to <https://openalex.org/settings/api> and sign in, or create a free account.
2. Copy your API key.
3. Paste it into the server's `.env`: `OPENALEX_API_KEY=...` and restart CiteWise.

### Crossref contact email (no key needed)

Crossref does not use API keys. If the host provides a contact email, usually their own, the server's requests go to Crossref's more reliable "polite pool":

```dotenv
CROSSREF_MAILTO=you@example.com
```

CiteWise also sends this email to OpenAlex as a contact address.

> Keys stay on the server. They are never sent to visitors' browsers, written to logs, or committed to Git (`.env` is git-ignored). Visitors can't see or extract the host's keys.

---

## Project layout

```
references-generator/
├── build/              # compiled executable (citewise.exe / citewise) goes here
├── code/               # all Go source code (the Go module)
│   ├── main.go         # entry point: config, SQLite, providers, HTTP server
│   └── internal/
│       ├── ai/         # OpenAI client (query generation, essay analysis, relevance)
│       ├── citation/   # APA 7 and Harvard formatting
│       ├── config/     # environment and .env loading
│       ├── essay/      # essay → claims → per-claim search → suggestions
│       ├── export/     # TXT and BibTeX export
│       ├── extract/    # PDF/DOCX/TXT validation and text extraction
│       ├── models/     # normalized publication model
│       ├── providers/  # Crossref and OpenAlex clients (+ retrying HTTP helper)
│       ├── search/     # query building, dedup, ranking, filtering
│       ├── store/      # SQLite persistence
│       └── web/        # handlers, html/template pages, CSS and JS (embedded)
├── build.bat           # Windows build script (Command Prompt / double-click)
├── build.ps1           # Windows build script (PowerShell)
├── build.sh            # macOS / Linux / Git Bash build script
└── .env.example        # configuration template
```

Templates, CSS and JavaScript are embedded in the executable, so `build/citewise.exe` runs on its own. You don't need Node.js, npm, a frontend build step, or a separate database server.

## Requirements

- **Go 1.26 or newer** (<https://go.dev/dl/>). Check with `go version`.
- An internet connection, to reach Crossref and OpenAlex (and OpenAI if configured).

SQLite uses a pure-Go driver (`modernc.org/sqlite`), so no C compiler or CGO is needed.

## Configuration

Copy the template and edit it:

```bash
cp .env.example .env          # Windows PowerShell: Copy-Item .env.example .env
```

| Variable | Default | Description |
|---|---|---|
| `OPENAI_API_KEY` | *(empty)* | Turns on AI features (see above) |
| `OPENAI_MODEL` | `gpt-4o-mini` | OpenAI chat model to use |
| `PORT` | `8080` | HTTP port |
| `DATABASE_PATH` | `./data/citewise.db` | SQLite file. The folder is created automatically |
| `MAX_UPLOAD_MB` | `10` | Maximum essay upload size (1 to 100) |
| `TEMP_DIR` | OS temp dir | Where uploads are stored briefly during processing |
| `CROSSREF_MAILTO` | *(empty)* | Contact email for the Crossref polite pool |
| `OPENALEX_API_KEY` | *(empty)* | OpenAlex API key |

CiteWise reads `.env` from the **current working directory**. Variables already set in the environment take priority over `.env`.

## Running

From the repository root, after building (see [Building the project](#building-the-project)):

```bash
./build/citewise.exe          # Windows (PowerShell or Git Bash)
./build/citewise              # macOS / Linux
```

Then open <http://localhost:8080>. Press `Ctrl+C` to stop the server.

During development you can run straight from source instead:

```bash
cd code
go run .
```

Note that `go run` uses `code/` as the working directory, so it reads `code/.env` and creates the database at `code/data/citewise.db`. Run the built executable from the repository root to use the root `.env` and `./data/`.

### Pages and routes

| Route | Purpose |
|---|---|
| `GET /` | Home page with recent searches |
| `GET/POST /references/search` | Title or topic search form, and running the search |
| `GET/POST /references/upload` | Essay upload form, and running the analysis |
| `GET /references/results/{id}` | Saved search results (`?style=apa` or `?style=harvard` switches style without searching again) |
| `GET /references/saved` | Saved references and previous searches |
| `POST /references/save` | Save, remove, approve or reject a reference |
| `POST /references/export` | Download selected references as TXT or BibTeX |
| `GET /health` | JSON health check (`{"status":"ok","database":"ok",...}`) |

## Running the tests

```bash
cd code
go test ./...                                # whole suite
go test ./internal/citation                  # one package
go test ./internal/web -run TestUploadFlow   # one test
go test -v ./internal/search -run TestDedupe # verbose single test
```

The tests use temporary SQLite databases and mocked Crossref, OpenAlex and OpenAI servers. They **don't need API keys or network access** and cost nothing.

Full check before committing:

```bash
cd code
gofmt -l .        # should print nothing
go mod tidy
go vet ./...
go test ./...
go build ./...
```

## Database

- Location: `DATABASE_PATH`, default `./data/citewise.db` (relative to the working directory).
- Created and migrated automatically at startup. To start fresh, stop the server and delete the `data/` folder.
- Tables: `searches`, `documents`, `publications`, `search_results`, `saved_references`. Foreign keys are on, the database uses WAL mode, and every query is parameterized.
- `data/` is git-ignored.

## Supported formats

- **Citation styles:** APA 7th edition and Harvard (Cite Them Right conventions). Citations are built only from the retrieved metadata. Missing details are left out or shown as `n.d.` / `no date`, never guessed. You can switch styles on any results page or on the saved-references page without searching again.
- **Upload formats:** PDF (text-based), DOCX and TXT (UTF-8 or UTF-16), up to `MAX_UPLOAD_MB`.
- **Export formats:** plain text (alphabetical reference list) and BibTeX (`.bib`).

## Privacy and storage

- Uploaded essays are written to a temporary file only while their text is extracted, then deleted immediately, including when something fails. The original file is never modified.
- **Essay text is not stored.** The database keeps only the file name, file size, processing status, an AI-written one-sentence summary of each identified claim, and the suggested publications.
- When AI analysis is on, the essay text (up to about 30,000 characters) is sent to OpenAI. Don't upload essays you aren't allowed to share with OpenAI.
- Essay content is treated as untrusted data. The AI is told to ignore any instructions inside the essay, and it can only pick from publications CiteWise has already retrieved, by ID. It cannot add bibliographic details.
- **There are no user accounts.** Saved references and search history live in the local database and are shared by everyone using the same installation.

## Known limitations

- No OCR. Scanned or image-only PDFs can't be read, and CiteWise says so.
- Older `.doc` files are not supported. Save them as `.docx`, `.pdf` or `.txt`.
- Relevance ranking is keyword-based (plus an AI check for essay claims). Always read a source before citing it.
- Titles are shown exactly as the providers return them. APA's sentence-case rule is not applied automatically, because doing so can break proper nouns.
- Author names from OpenAlex arrive as a single display name and are split into given and family names by a heuristic, so compound names can occasionally be split wrongly.
- Provider metadata is sometimes incomplete or inconsistent, for example missing pages or preprint versions. CiteWise shows what exists and does not fill gaps.
- Only APA 7 and Harvard styles, and only TXT and BibTeX exports. DOCX, RIS and other formats are not implemented.
- There is no authentication or per-user separation, so this is designed for local or single-group use.
- Essays longer than about 30,000 characters are analysed only up to that point, and the results page notes this.

---

## Building the project

The Go source lives in `code/`, and the compiled executable goes in `build/`.

### Option 1: build scripts (recommended)

**Windows (Command Prompt)**. Double-click `build.bat` in File Explorer, or run it from the repository root:

```bat
build.bat            :: builds build\citewise.exe
build.bat test       :: runs gofmt check, go vet and go test first, then builds
```

`build.bat` works from any directory and isn't affected by PowerShell's execution policy.

**Windows (PowerShell)**, from the repository root:

```powershell
.\build.ps1          # builds build\citewise.exe
.\build.ps1 -Test    # runs gofmt check, go vet and go test first, then builds
```

If PowerShell blocks the script, run it once with `powershell -ExecutionPolicy Bypass -File .\build.ps1`.

**macOS / Linux / Git Bash**, from the repository root:

```bash
chmod +x build.sh    # first time only
./build.sh           # builds build/citewise (build/citewise.exe on Windows)
./build.sh --test    # runs gofmt check, go vet and go test first, then builds
```

### Option 2: plain Go commands

```bash
cd code
go mod download                                   # fetch dependencies (first time)
go build -o ../build/citewise.exe .               # Windows
go build -o ../build/citewise .                   # macOS / Linux
```

### Cross-compiling

The app is pure Go, so you can build for another operating system from any machine:

```bash
cd code
GOOS=linux   GOARCH=amd64 CGO_ENABLED=0 go build -o ../build/citewise-linux-amd64 .
GOOS=darwin  GOARCH=arm64 CGO_ENABLED=0 go build -o ../build/citewise-macos-arm64 .
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -o ../build/citewise.exe .
```

In PowerShell, set the variables first: `$env:GOOS="linux"; $env:GOARCH="amd64"; $env:CGO_ENABLED="0"; go build -o ../build/citewise-linux-amd64 .`

### After building

1. Create `.env` in the repository root (see [Configuration](#configuration)).
2. From the repository root, run `./build/citewise.exe` (Windows) or `./build/citewise` (macOS/Linux).
3. Open <http://localhost:8080>.

The `build/` folder is kept in Git, but the executables inside it are git-ignored. Rebuild them after pulling new code.
