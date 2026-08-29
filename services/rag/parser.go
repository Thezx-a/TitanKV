package rag

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ParsedDocument is text extracted from an uploaded file.
type ParsedDocument struct {
	Text    string
	DocType string // markdown|pdf|plain|code|faq
	Pages   []ParsedPage
}

// ParsedPage holds optional per-page text for PDFs.
type ParsedPage struct {
	PageNum int
	Text    string
}

// ParseDocument detects type from filename and extracts UTF-8 text.
// PDFs use RAG_PDF_COMMAND (default pdftotext); missing command fails closed.
func ParseDocument(filename string, raw []byte) (*ParsedDocument, error) {
	return ParseDocumentWithCommand(filename, raw, getenv("RAG_PDF_COMMAND", "pdftotext"))
}

// ParseDocumentWithCommand is like ParseDocument but injectable for tests.
func ParseDocumentWithCommand(filename string, raw []byte, pdfCmd string) (*ParsedDocument, error) {
	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".pdf":
		return parsePDF(raw, pdfCmd)
	case ".md", ".markdown":
		return &ParsedDocument{Text: string(raw), DocType: "markdown"}, nil
	case ".go", ".cpp", ".h", ".hpp", ".c", ".py", ".js", ".ts", ".java", ".rs":
		return &ParsedDocument{Text: string(raw), DocType: "code"}, nil
	default:
		return &ParsedDocument{Text: string(raw), DocType: "plain"}, nil
	}
}

func parsePDF(raw []byte, pdfCmd string) (*ParsedDocument, error) {
	if pdfCmd == "" {
		pdfCmd = "pdftotext"
	}
	if _, err := exec.LookPath(pdfCmd); err != nil {
		return nil, fmt.Errorf("pdf parse: %s not found (%w); install poppler-utils or set RAG_PDF_COMMAND", pdfCmd, err)
	}
	tmp, err := os.CreateTemp("", "rag-pdf-*.pdf")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return nil, err
	}
	_ = tmp.Close()

	outPath := tmp.Name() + ".txt"
	defer os.Remove(outPath)

	// pdftotext -layout in.pdf out.txt
	cmd := exec.Command(pdfCmd, "-layout", tmp.Name(), outPath)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("pdf parse: %s failed: %v (%s)", pdfCmd, err, strings.TrimSpace(stderr.String()))
	}
	b, err := os.ReadFile(outPath)
	if err != nil {
		return nil, err
	}
	text := string(b)
	pages := splitPDFPages(text)
	return &ParsedDocument{Text: text, DocType: "pdf", Pages: pages}, nil
}

// splitPDFPages splits on form-feed if present (pdftotext default page break).
func splitPDFPages(text string) []ParsedPage {
	parts := strings.Split(text, "\f")
	out := make([]ParsedPage, 0, len(parts))
	for i, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, ParsedPage{PageNum: i + 1, Text: p})
	}
	return out
}
