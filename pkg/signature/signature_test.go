package signature

import (
	"bytes"
	"testing"
)

// --- ForTypes tests ---

func TestForTypesSelectsRequestedSignatures(t *testing.T) {
	sigs, err := ForTypes("pdf,zip,pdf")
	if err != nil {
		t.Fatalf("ForTypes returned error: %v", err)
	}
	if len(sigs) != 2 {
		t.Fatalf("expected 2 signatures, got %d", len(sigs))
	}
	if sigs[0].Name != "PDF" || sigs[1].Name != "ZIP" {
		t.Fatalf("unexpected signatures: %s, %s", sigs[0].Name, sigs[1].Name)
	}
}

func TestForTypesMapsOfficeTypesToZipSignature(t *testing.T) {
	sigs, err := ForTypes("docx,xlsx,pptx")
	if err != nil {
		t.Fatalf("ForTypes returned error: %v", err)
	}
	if len(sigs) != 1 {
		t.Fatalf("expected 1 raw signature, got %d", len(sigs))
	}
	if sigs[0].Name != "ZIP" {
		t.Fatalf("expected ZIP raw signature, got %s", sigs[0].Name)
	}
}

func TestForTypesRejectsUnsupportedTypes(t *testing.T) {
	if _, err := ForTypes("jpg"); err == nil {
		t.Fatal("expected unsupported jpg type to be rejected")
	}
}

func TestForTypesEmptyReturnsAll(t *testing.T) {
	sigs, err := ForTypes("")
	if err != nil {
		t.Fatalf("ForTypes('') returned error: %v", err)
	}
	if len(sigs) != len(Registry) {
		t.Fatalf("expected %d signatures (all), got %d", len(Registry), len(sigs))
	}
}

func TestForTypesWhitespaceReturnsAll(t *testing.T) {
	sigs, err := ForTypes("   ")
	if err != nil {
		t.Fatalf("ForTypes whitespace returned error: %v", err)
	}
	if len(sigs) != len(Registry) {
		t.Fatalf("expected all signatures, got %d", len(sigs))
	}
}

func TestForTypesCaseInsensitive(t *testing.T) {
	sigs, err := ForTypes("PDF,ZIP")
	if err != nil {
		t.Fatalf("ForTypes returned error: %v", err)
	}
	if len(sigs) != 2 {
		t.Fatalf("expected 2 signatures, got %d", len(sigs))
	}
}

func TestForTypesDeduplicate(t *testing.T) {
	// docx and xlsx both map to ZIP signature — should only get 1
	sigs, err := ForTypes("docx,xlsx,zip")
	if err != nil {
		t.Fatalf("ForTypes returned error: %v", err)
	}
	if len(sigs) != 1 {
		t.Fatalf("expected 1 signature (deduped ZIP), got %d", len(sigs))
	}
}

func TestForTypesSinglePDF(t *testing.T) {
	sigs, err := ForTypes("pdf")
	if err != nil {
		t.Fatalf("ForTypes returned error: %v", err)
	}
	if len(sigs) != 1 || sigs[0].Name != "PDF" {
		t.Fatalf("expected single PDF signature, got %v", sigs)
	}
}

func TestForTypesWithTrailingComma(t *testing.T) {
	sigs, err := ForTypes("pdf,")
	if err != nil {
		t.Fatalf("ForTypes returned error: %v", err)
	}
	if len(sigs) != 1 || sigs[0].Name != "PDF" {
		t.Fatalf("expected single PDF, got %v", sigs)
	}
}

// --- MatchHeader tests ---

func TestMatchHeader_PDF(t *testing.T) {
	sig := Registry[0] // PDF
	data := []byte("%PDF-1.4 rest of content")
	if !sig.MatchHeader(data) {
		t.Error("expected PDF header to match")
	}
}

func TestMatchHeader_PDFExact(t *testing.T) {
	sig := Registry[0]
	// Exactly the header bytes, nothing more
	if !sig.MatchHeader(sig.Header) {
		t.Error("expected exact header bytes to match")
	}
}

func TestMatchHeader_PDFMismatch(t *testing.T) {
	sig := Registry[0]
	data := []byte("Not a PDF file")
	if sig.MatchHeader(data) {
		t.Error("expected non-PDF data to NOT match")
	}
}

func TestMatchHeader_TooShort(t *testing.T) {
	sig := Registry[0]
	data := []byte("%PD") // Too short for 4-byte header
	if sig.MatchHeader(data) {
		t.Error("expected short data to NOT match")
	}
}

func TestMatchHeader_Empty(t *testing.T) {
	sig := Registry[0]
	if sig.MatchHeader(nil) {
		t.Error("expected nil data to NOT match")
	}
	if sig.MatchHeader([]byte{}) {
		t.Error("expected empty data to NOT match")
	}
}

func TestMatchHeader_ZIP(t *testing.T) {
	sig := Registry[1] // ZIP
	data := []byte{0x50, 0x4B, 0x03, 0x04, 0x00, 0x00}
	if !sig.MatchHeader(data) {
		t.Error("expected ZIP header to match")
	}
}

func TestMatchHeader_ZIPMismatch(t *testing.T) {
	sig := Registry[1]
	data := []byte{0x50, 0x4B, 0x05, 0x06} // This is EOCD, not local header
	if sig.MatchHeader(data) {
		t.Error("expected EOCD signature to NOT match local file header")
	}
}

func TestMatchHeader_PartialOverlap(t *testing.T) {
	sig := Registry[0] // PDF header is %PDF (25 50 44 46)
	data := []byte{0x25, 0x50, 0x44, 0x00} // First 3 bytes match, 4th doesn't
	if sig.MatchHeader(data) {
		t.Error("expected partial match to NOT match")
	}
}

// --- FindFooter tests ---

func TestFindFooter_PDFAtEnd(t *testing.T) {
	sig := Registry[0]
	data := []byte("%PDF-1.4 content here %%EOF")
	pos := sig.FindFooter(data)
	if pos != int64(len(data)) {
		t.Errorf("expected footer at %d, got %d", len(data), pos)
	}
}

func TestFindFooter_PDFMultipleEOF(t *testing.T) {
	sig := Registry[0]
	// FindFooter searches backwards, so should find the LAST %%EOF
	data := []byte("%PDF-1.4 %%EOF middle content %%EOF")
	pos := sig.FindFooter(data)
	expected := int64(len(data))
	if pos != expected {
		t.Errorf("expected last footer at %d, got %d", expected, pos)
	}
}

func TestFindFooter_PDFNotFound(t *testing.T) {
	sig := Registry[0]
	data := []byte("%PDF-1.4 content without end marker")
	pos := sig.FindFooter(data)
	if pos != -1 {
		t.Errorf("expected -1 for missing footer, got %d", pos)
	}
}

func TestFindFooter_ZIPAtEnd(t *testing.T) {
	sig := Registry[1]
	data := append([]byte("PK\x03\x04 zip content "), []byte{0x50, 0x4B, 0x05, 0x06}...)
	pos := sig.FindFooter(data)
	if pos != int64(len(data)) {
		t.Errorf("expected footer at %d, got %d", len(data), pos)
	}
}

func TestFindFooter_EmptyFooter(t *testing.T) {
	// A signature with no footer should return -1
	sig := FileSignature{
		Name:   "Test",
		Header: []byte{0xFF},
		Footer: nil,
	}
	pos := sig.FindFooter([]byte("anything"))
	if pos != -1 {
		t.Errorf("expected -1 for nil footer, got %d", pos)
	}
}

func TestFindFooter_FooterAtStart(t *testing.T) {
	sig := Registry[0]
	data := []byte("%%EOF rest of content")
	pos := sig.FindFooter(data)
	// Search backwards, last occurrence of %%EOF is at start
	if pos != 5 { // len("%%EOF") = 5
		t.Errorf("expected footer position 5, got %d", pos)
	}
}

func TestFindFooter_DataSmallerThanFooter(t *testing.T) {
	sig := Registry[0] // footer is 5 bytes
	data := []byte("%%E")
	pos := sig.FindFooter(data)
	if pos != -1 {
		t.Errorf("expected -1 for data smaller than footer, got %d", pos)
	}
}

// --- Registry tests ---

func TestRegistryHasExpectedSignatures(t *testing.T) {
	if len(Registry) < 2 {
		t.Fatalf("expected at least 2 signatures in registry, got %d", len(Registry))
	}

	// Verify PDF signature
	pdf := Registry[0]
	if pdf.Name != "PDF" {
		t.Errorf("first signature name = %q, want PDF", pdf.Name)
	}
	if pdf.Extension != ".pdf" {
		t.Errorf("PDF extension = %q, want .pdf", pdf.Extension)
	}
	if !bytes.Equal(pdf.Header, []byte{0x25, 0x50, 0x44, 0x46}) {
		t.Errorf("PDF header = %v, want %%PDF bytes", pdf.Header)
	}
	if !bytes.Equal(pdf.Footer, []byte{0x25, 0x25, 0x45, 0x4F, 0x46}) {
		t.Errorf("PDF footer = %v, want %%%%EOF bytes", pdf.Footer)
	}
	if pdf.MaxSize <= 0 {
		t.Error("PDF MaxSize should be positive")
	}

	// Verify ZIP signature
	zip := Registry[1]
	if zip.Name != "ZIP" {
		t.Errorf("second signature name = %q, want ZIP", zip.Name)
	}
	if zip.Extension != ".zip" {
		t.Errorf("ZIP extension = %q, want .zip", zip.Extension)
	}
	if !bytes.Equal(zip.Header, []byte{0x50, 0x4B, 0x03, 0x04}) {
		t.Errorf("ZIP header = %v, want PK\\x03\\x04 bytes", zip.Header)
	}
	if !bytes.Equal(zip.Footer, []byte{0x50, 0x4B, 0x05, 0x06}) {
		t.Errorf("ZIP footer = %v, want PK\\x05\\x06 bytes", zip.Footer)
	}
	if zip.MaxSize <= 0 {
		t.Error("ZIP MaxSize should be positive")
	}
}

func TestRegistrySignaturesAreIndependent(t *testing.T) {
	// Modifying the result of ForTypes should not modify the Registry
	sigs, _ := ForTypes("")
	originalName := Registry[0].Name
	sigs[0].Name = "MODIFIED"
	if Registry[0].Name != originalName {
		t.Error("ForTypes should return a copy, not reference to Registry")
	}
}

// --- MaxSize sanity tests ---

func TestMaxSizeReasonable(t *testing.T) {
	for _, sig := range Registry {
		if sig.MaxSize < 1024 {
			t.Errorf("signature %s has unreasonably small MaxSize: %d", sig.Name, sig.MaxSize)
		}
		if sig.MaxSize > 10*1024*1024*1024 { // 10GB
			t.Errorf("signature %s has unreasonably large MaxSize: %d", sig.Name, sig.MaxSize)
		}
	}
}
