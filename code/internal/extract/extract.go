// Package extract validates uploaded essays and extracts their text. Uploads
// are written to a temporary file that is always removed after processing.
package extract

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/ledongthuc/pdf"
)

// Kind is a supported document type.
type Kind string

// Supported kinds.
const (
	PDF  Kind = "pdf"
	DOCX Kind = "docx"
	TXT  Kind = "txt"
)

// Limits on extracted text.
const (
	MinTextChars = 200
	MaxTextChars = 60000
	// maxXMLBytes guards against zip bombs in DOCX files.
	maxXMLBytes = 30 << 20
)

// UserError is a problem that should be shown to the user verbatim.
type UserError struct{ Msg string }

func (e *UserError) Error() string { return e.Msg }

func userErr(format string, args ...any) error { return &UserError{Msg: fmt.Sprintf(format, args...)} }

// IsUserError reports whether err carries a user-facing message.
func IsUserError(err error) (string, bool) {
	var ue *UserError
	if errors.As(err, &ue) {
		return ue.Msg, true
	}
	return "", false
}

// Result is the outcome of processing an upload.
type Result struct {
	Kind      Kind
	Text      string
	Truncated bool
}

// KindFromName returns the document kind for a filename's extension.
func KindFromName(name string) (Kind, error) {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".pdf":
		return PDF, nil
	case ".docx":
		return DOCX, nil
	case ".txt":
		return TXT, nil
	case ".doc":
		return "", userErr("Older .doc files are not supported. Save the essay as .docx, .pdf or .txt and try again.")
	}
	return "", userErr("Unsupported file type. Upload a PDF, DOCX or TXT file.")
}

// CheckSignature verifies that the file content matches its extension.
func CheckSignature(kind Kind, head []byte) error {
	switch kind {
	case PDF:
		if !bytes.HasPrefix(head, []byte("%PDF-")) {
			return userErr("The file does not look like a valid PDF.")
		}
	case DOCX:
		if !bytes.HasPrefix(head, []byte("PK\x03\x04")) {
			return userErr("The file does not look like a valid DOCX document.")
		}
	case TXT:
		if bytes.HasPrefix(head, []byte{0xFF, 0xFE}) || bytes.HasPrefix(head, []byte{0xFE, 0xFF}) {
			return nil // UTF-16 with BOM
		}
		if bytes.IndexByte(head, 0) >= 0 {
			return userErr("The text file appears to be binary. Upload a plain-text (.txt) file.")
		}
	}
	return nil
}

// Process validates an upload, writes it to a temporary file in tempDir,
// extracts its text and removes the temporary file before returning.
func Process(tempDir, filename string, r io.Reader, maxBytes int64) (Result, error) {
	kind, err := KindFromName(filename)
	if err != nil {
		return Result{}, err
	}
	path, size, err := saveTemp(tempDir, r, maxBytes, string(kind))
	if path != "" {
		defer os.Remove(path)
	}
	if err != nil {
		return Result{}, err
	}
	if size == 0 {
		return Result{}, userErr("The uploaded file is empty.")
	}
	head, err := readHead(path, 512)
	if err != nil {
		return Result{}, err
	}
	if err := CheckSignature(kind, head); err != nil {
		return Result{}, err
	}
	text, err := extractFile(path, kind)
	if err != nil {
		return Result{}, err
	}
	text = Clean(text)
	if len([]rune(text)) < MinTextChars {
		if kind == PDF {
			return Result{}, userErr("Very little text could be extracted from this PDF. It may be a scanned image; scanned PDFs (OCR) are not supported. Try exporting the essay as DOCX or TXT.")
		}
		return Result{}, userErr("The document contains too little text to analyse (at least %d characters are needed).", MinTextChars)
	}
	res := Result{Kind: kind, Text: text}
	if r := []rune(text); len(r) > MaxTextChars {
		res.Text = string(r[:MaxTextChars])
		res.Truncated = true
	}
	return res, nil
}

func saveTemp(dir string, r io.Reader, maxBytes int64, ext string) (string, int64, error) {
	f, err := os.CreateTemp(dir, "citewise-upload-*."+ext)
	if err != nil {
		return "", 0, fmt.Errorf("create temp file: %w", err)
	}
	n, err := io.Copy(f, io.LimitReader(r, maxBytes+1))
	cerr := f.Close()
	if err != nil {
		return f.Name(), n, fmt.Errorf("write temp file: %w", err)
	}
	if cerr != nil {
		return f.Name(), n, cerr
	}
	if n > maxBytes {
		return f.Name(), n, userErr("The file is larger than the %d MB limit.", maxBytes>>20)
	}
	return f.Name(), n, nil
}

func readHead(path string, n int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, n)
	m, err := io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return buf[:m], nil
}

func extractFile(path string, kind Kind) (string, error) {
	switch kind {
	case PDF:
		return extractPDF(path)
	case DOCX:
		return extractDOCX(path)
	default:
		b, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		return decodeText(b)
	}
}

func extractPDF(path string) (text string, err error) {
	// The PDF parser can panic on malformed input; convert that to an error.
	defer func() {
		if r := recover(); r != nil {
			text, err = "", userErr("The PDF could not be read. It may be damaged, encrypted or use unsupported features.")
		}
	}()
	f, r, err := pdf.Open(path)
	if err != nil {
		return "", userErr("The PDF could not be read. It may be damaged or password-protected.")
	}
	defer f.Close()
	rd, err := r.GetPlainText()
	if err != nil {
		return "", userErr("Text could not be extracted from this PDF.")
	}
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, io.LimitReader(rd, maxXMLBytes)); err != nil {
		return "", userErr("Text could not be extracted from this PDF.")
	}
	return buf.String(), nil
}

func extractDOCX(path string) (string, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return "", userErr("The DOCX file could not be opened. It may be damaged.")
	}
	defer zr.Close()
	var doc *zip.File
	for _, f := range zr.File {
		if f.Name == "word/document.xml" {
			doc = f
			break
		}
	}
	if doc == nil {
		return "", userErr("The DOCX file has no document body. It may be damaged or not a Word document.")
	}
	rc, err := doc.Open()
	if err != nil {
		return "", userErr("The DOCX file could not be read.")
	}
	defer rc.Close()

	var b strings.Builder
	dec := xml.NewDecoder(io.LimitReader(rc, maxXMLBytes))
	inText := false
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", userErr("The DOCX document body could not be parsed.")
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "t":
				inText = true
			case "tab":
				b.WriteByte('\t')
			case "br", "cr":
				b.WriteByte('\n')
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inText = false
			case "p":
				b.WriteString("\n\n")
			}
		case xml.CharData:
			if inText {
				b.Write(t)
			}
		}
	}
	return b.String(), nil
}

func decodeText(b []byte) (string, error) {
	switch {
	case bytes.HasPrefix(b, []byte{0xEF, 0xBB, 0xBF}):
		b = b[3:]
	case bytes.HasPrefix(b, []byte{0xFF, 0xFE}), bytes.HasPrefix(b, []byte{0xFE, 0xFF}):
		le := b[0] == 0xFF
		b = b[2:]
		u := make([]uint16, len(b)/2)
		for i := range u {
			if le {
				u[i] = uint16(b[2*i]) | uint16(b[2*i+1])<<8
			} else {
				u[i] = uint16(b[2*i])<<8 | uint16(b[2*i+1])
			}
		}
		return string(utf16.Decode(u)), nil
	}
	if !utf8.Valid(b) {
		return "", userErr("The text file is not valid UTF-8. Save it with UTF-8 encoding and try again.")
	}
	return string(b), nil
}

// Clean normalizes line endings, removes control characters and collapses
// runs of blank lines and spaces.
func Clean(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	var b strings.Builder
	for _, r := range s {
		if r == '\n' || r == '\t' || r >= 0x20 && r != 0x7f && r != utf8.RuneError {
			b.WriteRune(r)
		}
	}
	lines := strings.Split(b.String(), "\n")
	var out []string
	blank := 0
	for _, l := range lines {
		l = strings.Join(strings.Fields(l), " ")
		if l == "" {
			blank++
			if blank == 1 && len(out) > 0 {
				out = append(out, "")
			}
			continue
		}
		blank = 0
		out = append(out, l)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}
