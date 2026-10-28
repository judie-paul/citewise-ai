package extract

import (
	"archive/zip"
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"
)

var essay = strings.Repeat("Climate change is reducing crop yields across sub-Saharan Africa. ", 8)

func assertEmptyDir(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("temporary files were not cleaned up: %d left", len(entries))
	}
}

func userMsg(t *testing.T, err error) string {
	t.Helper()
	msg, ok := IsUserError(err)
	if !ok {
		t.Fatalf("expected user error, got %v", err)
	}
	return msg
}

func makeDOCX(t *testing.T, paragraphs ...string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("word/document.xml")
	var body strings.Builder
	for _, p := range paragraphs {
		fmt.Fprintf(&body, `<w:p><w:r><w:t xml:space="preserve">%s</w:t></w:r></w:p>`, p)
	}
	fmt.Fprintf(w, `<?xml version="1.0"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>%s</w:body></w:document>`, body.String())
	zw.Close()
	return buf.Bytes()
}

// makePDF builds a minimal single-page PDF with valid xref offsets.
func makePDF(text string) []byte {
	content := fmt.Sprintf("BT /F1 12 Tf 72 720 Td (%s) Tj ET", text)
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objs))
	for i, o := range objs {
		offsets[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for _, off := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, xref)
	return b.Bytes()
}

func TestKindFromName(t *testing.T) {
	for name, want := range map[string]Kind{"a.PDF": PDF, "b.docx": DOCX, "c.txt": TXT} {
		if got, err := KindFromName(name); err != nil || got != want {
			t.Errorf("KindFromName(%q) = %q, %v", name, got, err)
		}
	}
	for _, bad := range []string{"a.exe", "b.doc", "noext", "x.pdf.exe"} {
		if _, err := KindFromName(bad); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
}

func TestProcessTXT(t *testing.T) {
	dir := t.TempDir()
	res, err := Process(dir, "essay.txt", strings.NewReader("\xEF\xBB\xBF"+essay+"\r\n\r\n\r\nSecond   paragraph."), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if res.Kind != TXT || !strings.HasSuffix(res.Text, "\n\nSecond paragraph.") || strings.HasPrefix(res.Text, string([]byte{0xEF, 0xBB, 0xBF})) {
		t.Fatalf("unexpected text: %q", res.Text)
	}
	assertEmptyDir(t, dir)
}

func TestProcessUTF16TXT(t *testing.T) {
	dir := t.TempDir()
	var b bytes.Buffer
	b.Write([]byte{0xFF, 0xFE})
	for _, r := range essay {
		b.WriteByte(byte(r))
		b.WriteByte(0)
	}
	res, err := Process(dir, "essay.txt", &b, 1<<20)
	if err != nil || !strings.HasPrefix(res.Text, "Climate change") {
		t.Fatalf("UTF-16: %v %q", err, res.Text)
	}
}

func TestProcessDOCX(t *testing.T) {
	dir := t.TempDir()
	res, err := Process(dir, "essay.docx", bytes.NewReader(makeDOCX(t, essay, "Conclusion &amp; outlook.")), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "\n\nConclusion & outlook.") {
		t.Fatalf("DOCX text: %q", res.Text)
	}
	assertEmptyDir(t, dir)
}

func TestProcessPDF(t *testing.T) {
	dir := t.TempDir()
	res, err := Process(dir, "essay.pdf", bytes.NewReader(makePDF(essay)), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "Climate change is reducing crop yields") {
		t.Fatalf("PDF text: %q", res.Text)
	}
	assertEmptyDir(t, dir)
}

func TestProcessRejections(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name, file string
		data       []byte
		max        int64
		want       string
	}{
		{"too large", "a.txt", []byte(essay + essay), 100, "larger than"},
		{"empty", "a.txt", nil, 1 << 20, "empty"},
		{"fake pdf", "a.pdf", []byte("hello world, not a pdf"), 1 << 20, "valid PDF"},
		{"fake docx", "a.docx", []byte("plain text"), 1 << 20, "valid DOCX"},
		{"binary txt", "a.txt", []byte("abc\x00def"), 1 << 20, "binary"},
		{"invalid utf8", "a.txt", []byte(essay + "\xff\xfe\xfd"), 1 << 20, "UTF-8"},
		{"too short", "a.txt", []byte("Short essay."), 1 << 20, "too little text"},
		{"broken pdf", "a.pdf", []byte("%PDF-1.4\ngarbage"), 1 << 20, "PDF"},
		{"zip without body", "a.docx", func() []byte {
			var b bytes.Buffer
			zw := zip.NewWriter(&b)
			zw.Create("other.txt")
			zw.Close()
			return b.Bytes()
		}(), 1 << 20, "no document body"},
		{"scanned pdf", "a.pdf", makePDF(""), 1 << 20, "scanned"},
		{"bad type", "a.exe", []byte("MZ"), 1 << 20, "Unsupported"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Process(dir, c.file, bytes.NewReader(c.data), c.max)
			if msg := userMsg(t, err); !strings.Contains(msg, c.want) {
				t.Fatalf("message %q does not contain %q", msg, c.want)
			}
		})
	}
	assertEmptyDir(t, dir)
}

func TestTruncation(t *testing.T) {
	dir := t.TempDir()
	long := strings.Repeat("word ", MaxTextChars/4)
	res, err := Process(dir, "long.txt", strings.NewReader(long), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Truncated || len([]rune(res.Text)) != MaxTextChars {
		t.Fatalf("truncated=%v len=%d", res.Truncated, len([]rune(res.Text)))
	}
}

func TestClean(t *testing.T) {
	got := Clean("a\x07b  c\r\n\r\n\r\n\r\nd\te")
	if got != "ab c\n\nd e" {
		t.Fatalf("Clean = %q", got)
	}
}
