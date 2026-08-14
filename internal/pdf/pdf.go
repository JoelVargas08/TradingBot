package pdf

import (
	"bytes"
	"compress/zlib"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// Extractor extrae texto plano de un PDF usando solo la biblioteca estándar.
// Soporta streams comprimidos FlateDecode y operadores de texto comunes
// (Tj, TJ, ', "). No depende de poppler ni de bibliotecas externas.
type Extractor struct {
	maxStreamBytes int64
}

func New() *Extractor {
	return &Extractor{maxStreamBytes: 64 << 20}
}

var ErrNotPDF = errors.New("el archivo no parece un PDF válido")

func (e *Extractor) ExtractText(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("abriendo PDF: %w", err)
	}
	defer f.Close()
	return e.ExtractTextFromReader(f)
}

func (e *Extractor) ExtractTextFromReader(r io.Reader) (string, error) {
	data, err := io.ReadAll(io.LimitReader(r, e.maxStreamBytes))
	if err != nil {
		return "", err
	}
	if !looksLikePDF(data) {
		return "", ErrNotPDF
	}
	var out strings.Builder
	for _, stream := range findStreams(data) {
		content, err := decodeStream(stream)
		if err != nil {
			continue
		}
		out.WriteString(extractTextFromContent(content))
		out.WriteString("\n")
	}
	return out.String(), nil
}

func looksLikePDF(data []byte) bool {
	header := data
	if len(header) > 1024 {
		header = header[:1024]
	}
	return bytes.Contains(header, []byte("%PDF-"))
}

type rawStream struct {
	data       []byte
	dictionary []byte
}

// findStreams localiza los bloques "stream ... endstream" y captura su
// diccionario inmediatamente anterior.
func findStreams(data []byte) []rawStream {
	var streams []rawStream
	pos := 0
	for pos < len(data) {
		idx := bytes.Index(data[pos:], []byte("stream"))
		if idx < 0 {
			break
		}
		start := pos + idx
		if start+6 < len(data) && (data[start+6] == '\r' || data[start+6] == '\n') {
			dictStart := maxInt(0, start-512)
			dict := data[dictStart:start]
			end := bytes.Index(data[start+6:], []byte("endstream"))
			if end < 0 {
				break
			}
			contentStart := start + 6
			if data[contentStart] == '\r' {
				contentStart++
			}
			if contentStart < len(data) && data[contentStart] == '\n' {
				contentStart++
			}
			contentEnd := start + 6 + end
			// recortar el \r\n o \n que precede a endstream
			for contentEnd > contentStart && (data[contentEnd-1] == '\n' || data[contentEnd-1] == '\r') {
				contentEnd--
			}
			streams = append(streams, rawStream{
				data:       data[contentStart:contentEnd],
				dictionary: dict,
			})
			pos = contentEnd + len("endstream")
			continue
		}
		pos = start + len("stream")
	}
	return streams
}

// decodeStream descomprime el stream según su filtro (FlateDecode) y,
// si no tiene filtro, lo devuelve tal cual.
func decodeStream(s rawStream) ([]byte, error) {
	dict := string(s.dictionary)
	if strings.Contains(dict, "/FlateDecode") || strings.Contains(dict, "/Fl") {
		zr, err := zlib.NewReader(bytes.NewReader(s.data))
		if err != nil {
			return nil, err
		}
		defer zr.Close()
		out, err := io.ReadAll(io.LimitReader(zr, 64<<20))
		if err != nil {
			return nil, err
		}
		return out, nil
	}
	return s.data, nil
}

// extractTextFromContent procesa el content stream y extrae las cadenas
// mostradas por los operadores Tj / TJ / ' / ".
func extractTextFromContent(content []byte) string {
	var out strings.Builder
	i := 0
	n := len(content)
	for i < n {
		// saltar espacios y saltos de línea
		for i < n && isSpace(content[i]) {
			i++
		}
		if i >= n {
			break
		}
		switch content[i] {
		case '(':
			start := i
			if s, next, ok := parseLiteral(content, i); ok {
				i = next
				out.WriteString(s)
				// si el siguiente token es Tj o ' o " consumimos el operador
				op := nextOperator(content, i)
				switch op {
				case "Tj", "'":
					out.WriteString(" ")
					i += len(op)
				case "\"":
					i += len(op)
				case "TJ":
					// no aplica a literales simples
				}
				_ = start
			} else {
				i++
			}
		case '[':
			if s, next, ok := parseArray(content, i); ok {
				out.WriteString(s)
				i = next
				op := nextOperator(content, i)
				if op == "TJ" {
					i += len(op)
				}
			} else {
				i++
			}
		case '<':
			if hex, next, ok := parseHexString(content, i); ok {
				out.WriteString(hex)
				i = next
				op := nextOperator(content, i)
				if op == "Tj" || op == "'" {
					out.WriteString(" ")
					i += len(op)
				}
			} else {
				i++
			}
		case 'T':
			// "Tj", "TJ" pueden aparecer sin paréntesis previo cuando el texto
			// viene de una variable; en ese caso no podemos extraer la cadena.
			i++
		default:
			i++
		}
	}
	return strings.TrimSpace(out.String())
}

func parseLiteral(content []byte, start int) (string, int, bool) {
	// asume content[start] == '('
	i := start + 1
	depth := 1
	var sb strings.Builder
	n := len(content)
	for i < n && depth > 0 {
		c := content[i]
		switch c {
		case '\\':
			if i+1 >= n {
				return "", start, false
			}
			esc := content[i+1]
			switch esc {
			case 'n':
				sb.WriteByte('\n')
			case 'r':
				sb.WriteByte('\r')
			case 't':
				sb.WriteByte('\t')
			case 'b':
				sb.WriteByte('\b')
			case 'f':
				sb.WriteByte('\f')
			case '(', ')', '\\':
				sb.WriteByte(esc)
			default:
				if esc >= '0' && esc <= '7' {
					oct := string(esc)
					k := i + 2
					for len(oct) < 3 && k < n && content[k] >= '0' && content[k] <= '7' {
						oct += string(content[k])
						k++
					}
					v, _ := strconv.ParseUint(oct, 8, 8)
					sb.WriteByte(byte(v))
					i = k - 1
				} else {
					sb.WriteByte(esc)
				}
			}
			i += 2
		case '(':
			depth++
			sb.WriteByte('(')
			i++
		case ')':
			depth--
			if depth > 0 {
				sb.WriteByte(')')
			}
			i++
		default:
			sb.WriteByte(c)
			i++
		}
	}
	if depth != 0 {
		return "", start, false
	}
	return sb.String(), i, true
}

func parseArray(content []byte, start int) (string, int, bool) {
	// asume content[start] == '['
	i := start + 1
	n := len(content)
	var parts []string
	for i < n {
		for i < n && isSpace(content[i]) {
			i++
		}
		if i >= n {
			break
		}
		switch content[i] {
		case ']':
			return strings.Join(parts, ""), i + 1, true
		case '(':
			if s, next, ok := parseLiteral(content, i); ok {
				parts = append(parts, s)
				i = next
			} else {
				i++
			}
		case '<':
			if hex, next, ok := parseHexString(content, i); ok {
				parts = append(parts, hex)
				i = next
			} else {
				i++
			}
		default:
			// números de ajuste de posición, ignorar
			i++
		}
	}
	return "", start, false
}

func parseHexString(content []byte, start int) (string, int, bool) {
	// asume content[start] == '<'
	end := bytes.IndexByte(content[start+1:], '>')
	if end < 0 {
		return "", start, false
	}
	hexData := content[start+1 : start+1+end]
	var hex strings.Builder
	for _, c := range hexData {
		if !isSpace(c) {
			hex.WriteByte(c)
		}
	}
	if hex.Len()%2 != 0 {
		hex.WriteByte('0')
	}
	hs := hex.String()
	var sb strings.Builder
	for j := 0; j+1 < len(hs); j += 2 {
		v, err := strconv.ParseUint(hs[j:j+2], 16, 8)
		if err != nil {
			continue
		}
		sb.WriteByte(byte(v))
	}
	return sb.String(), start + 1 + end + 1, true
}

// nextOperator devuelve el siguiente operador (secuencia de letras) tras pos.
func nextOperator(content []byte, pos int) string {
	i := pos
	n := len(content)
	for i < n && isSpace(content[i]) {
		i++
	}
	start := i
	for i < n && isAlpha(content[i]) {
		i++
	}
	return string(content[start:i])
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v'
}

func isAlpha(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
