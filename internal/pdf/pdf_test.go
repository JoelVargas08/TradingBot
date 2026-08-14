package pdf

import (
	"bytes"
	"compress/zlib"
	"strconv"
	"strings"
	"testing"
)

// buildMinimalPDF construye un PDF válido con una página cuyo content stream
// está comprimido con FlateDecode y contiene texto.
func buildMinimalPDF(t *testing.T, text string) []byte {
	t.Helper()
	content := "BT /F1 12 Tf 72 712 Td (" + text + ") Tj ET"
	var buf bytes.Buffer
	zw := zlib.NewWriter(&buf)
	zw.Write([]byte(content))
	zw.Close()

	pdf := "%PDF-1.4\n" +
		"1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n" +
		"2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n" +
		"3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R >>\nendobj\n" +
		"4 0 obj\n<< /Length " + strconv.Itoa(buf.Len()) + " /Filter /FlateDecode >>\nstream\n" +
		buf.String() + "\nendstream\nendobj\n" +
		"trailer\n<< /Root 1 0 R /Size 5 >>\n%%EOF\n"
	return []byte(pdf)
}

func TestExtractTextFromReader(t *testing.T) {
	ex := New()
	got, err := ex.ExtractTextFromReader(bytes.NewReader(buildMinimalPDF(t, "RSI crossover buy")))
	if err != nil {
		t.Fatalf("ExtractTextFromReader: %v", err)
	}
	if !strings.Contains(got, "RSI crossover buy") {
		t.Errorf("texto extraído no contiene la cadena esperada: %q", got)
	}
}

func TestExtractTextRejectsNonPDF(t *testing.T) {
	ex := New()
	if _, err := ex.ExtractTextFromReader(bytes.NewReader([]byte("hola mundo"))); err != ErrNotPDF {
		t.Errorf("want ErrNotPDF, got %v", err)
	}
}

func TestParseLiteralEscapes(t *testing.T) {
	content := "(a\\(b\\)c\\\\d) Tj"
	got := extractTextFromContent([]byte(content))
	if got != "a(b)c\\d" {
		t.Errorf("got %q, want %q", got, "a(b)c\\d")
	}
}

func TestParseTJArray(t *testing.T) {
	content := "[(BUY) ( signal)] TJ"
	got := extractTextFromContent([]byte(content))
	if !strings.Contains(got, "BUY signal") {
		t.Errorf("got %q, want contener %q", got, "BUY signal")
	}
}

func TestParseHexString(t *testing.T) {
	content := "<42 55 59> Tj"
	got := extractTextFromContent([]byte(content))
	if got != "BUY" {
		t.Errorf("got %q, want %q", got, "BUY")
	}
}
