package carver

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// TestCarveFromDiskImage builds a simulated disk image with known files embedded
// at various offsets surrounded by random data, then runs the full carving
// pipeline and verifies the correct files are recovered.
func TestCarveFromDiskImage(t *testing.T) {
	// Load test files
	validPDF := mustReadFile(t, "testdata/valid.pdf")
	multipagePDF := mustReadFile(t, "testdata/multipage.pdf")
	validZIP := mustReadFile(t, "testdata/valid.zip")
	validDOCX := mustReadFile(t, "testdata/valid.docx")
	validXLSX := mustReadFile(t, "testdata/valid.xlsx")
	validPPTX := mustReadFile(t, "testdata/valid.pptx")

	// Build a 512KB simulated disk image
	const imageSize = 512 * 1024
	image := make([]byte, imageSize)

	// Fill with random data (simulates disk with deleted content)
	rand.Read(image)

	// Embed files at known offsets (aligned to 4096-byte boundaries like real clusters)
	type embeddedFile struct {
		offset int
		data   []byte
		name   string
	}
	files := []embeddedFile{
		{offset: 4096, data: validPDF, name: "valid.pdf"},
		{offset: 12288, data: multipagePDF, name: "multipage.pdf"},
		{offset: 24576, data: validZIP, name: "valid.zip"},
		{offset: 36864, data: validDOCX, name: "valid.docx"},
		{offset: 53248, data: validXLSX, name: "valid.xlsx"},
		{offset: 73728, data: validPPTX, name: "valid.pptx"},
	}

	for _, f := range files {
		if f.offset+len(f.data) > imageSize {
			t.Fatalf("file %s (offset %d, size %d) exceeds image size %d", f.name, f.offset, len(f.data), imageSize)
		}
		copy(image[f.offset:], f.data)
	}

	// Write disk image to temp file
	imgPath := filepath.Join(t.TempDir(), "test.img")
	if err := os.WriteFile(imgPath, image, 0o644); err != nil {
		t.Fatalf("failed to write test image: %v", err)
	}

	// Run carver
	outputDir := filepath.Join(t.TempDir(), "recovered")
	cfg := Config{
		DiskPath:  imgPath,
		OutputDir: outputDir,
		BlockSize: 4096,
		Verbose:   true,
	}

	c, err := New(cfg)
	if err != nil {
		t.Fatalf("failed to create carver: %v", err)
	}
	defer c.Close()

	if err := c.Scan(nil); err != nil {
		t.Fatalf("scan failed: %v", err)
	}

	results := c.Results()

	// We should recover at least the 2 PDFs and 4 ZIP-based files
	// (exact count may vary if random data doesn't contain false signatures)
	if len(results) < 6 {
		t.Errorf("expected at least 6 recovered files, got %d", len(results))
		for _, r := range results {
			t.Logf("  recovered: %s (%d bytes) at offset %d", r.Signature.Name, r.Size, r.Offset)
		}
	}

	// Verify we found specific types
	typeCounts := make(map[string]int)
	for _, r := range results {
		typeCounts[r.Signature.Name]++
	}

	t.Logf("Recovery results: %v", typeCounts)

	if typeCounts["PDF"] < 2 {
		t.Errorf("expected at least 2 PDFs recovered, got %d", typeCounts["PDF"])
	}

	// ZIP-family (ZIP + DOCX + XLSX + PPTX) should total at least 4
	zipFamily := typeCounts["ZIP"] + typeCounts["DOCX"] + typeCounts["XLSX"] + typeCounts["PPTX"]
	if zipFamily < 4 {
		t.Errorf("expected at least 4 ZIP-family files recovered, got %d", zipFamily)
	}

	// Verify recovered files are valid by reading them back
	for _, r := range results {
		data, err := os.ReadFile(r.OutputPath)
		if err != nil {
			t.Errorf("failed to read recovered file %s: %v", r.OutputPath, err)
			continue
		}
		if int64(len(data)) != r.Size {
			t.Errorf("recovered file %s: size mismatch (disk=%d, reported=%d)", r.OutputPath, len(data), r.Size)
		}
	}
}

// TestCarveRejectsGarbagePDF verifies that random data with %PDF header and
// %%EOF footer but no real PDF structure is rejected.
func TestCarveRejectsGarbagePDF(t *testing.T) {
	// Build an image with a fake PDF: header + random bytes + footer
	const imageSize = 64 * 1024
	image := make([]byte, imageSize)

	// Embed a fake PDF at offset 0
	fakePDF := []byte("%PDF-1.4\n")
	rand.Read(image[len(fakePDF) : 50000]) // random content
	copy(image[0:], fakePDF)
	copy(image[50000:], []byte("\nstartxref\n49000\n%%EOF\n"))
	// This has startxref + %%EOF but no actual obj/xref/trailer in the body

	imgPath := filepath.Join(t.TempDir(), "garbage.img")
	os.WriteFile(imgPath, image, 0o644)

	outputDir := filepath.Join(t.TempDir(), "recovered")
	cfg := Config{
		DiskPath:  imgPath,
		OutputDir: outputDir,
		BlockSize: 4096,
		Verbose:   false,
	}

	c, err := New(cfg)
	if err != nil {
		t.Fatalf("failed to create carver: %v", err)
	}
	defer c.Close()

	c.Scan(nil)
	results := c.Results()

	// Should NOT recover any PDF (garbage data with control chars fails validation)
	for _, r := range results {
		if r.Signature.Name == "PDF" {
			t.Errorf("should not have recovered garbage as PDF (size=%d, offset=%d)", r.Size, r.Offset)
		}
	}
}

// TestCarveRejectsCorruptZIP verifies that a ZIP with corrupted compressed data
// is rejected by the decompression validation.
func TestCarveRejectsCorruptZIP(t *testing.T) {
	// Create a valid ZIP then corrupt the compressed data
	validZIP := makeZip(t, map[string]string{
		"important.txt": "This is important document content that should be long enough to compress.",
	})

	// Corrupt bytes in the compressed data region (after local header)
	corruptZIP := append([]byte{}, validZIP...)
	// Local header is 30 + nameLen bytes. Corrupt after that.
	nameLen := binary.LittleEndian.Uint16(corruptZIP[26:28])
	dataStart := 30 + int(nameLen)
	if dataStart+10 < len(corruptZIP) {
		for i := dataStart; i < dataStart+10 && i < len(corruptZIP); i++ {
			corruptZIP[i] ^= 0xFF
		}
	}

	// Build image with corrupt ZIP
	const imageSize = 32 * 1024
	image := make([]byte, imageSize)
	rand.Read(image)
	copy(image[4096:], corruptZIP)

	imgPath := filepath.Join(t.TempDir(), "corrupt_zip.img")
	os.WriteFile(imgPath, image, 0o644)

	outputDir := filepath.Join(t.TempDir(), "recovered")
	cfg := Config{
		DiskPath:  imgPath,
		OutputDir: outputDir,
		BlockSize: 4096,
		Verbose:   false,
	}

	c, err := New(cfg)
	if err != nil {
		t.Fatalf("failed to create carver: %v", err)
	}
	defer c.Close()

	c.Scan(nil)
	results := c.Results()

	for _, r := range results {
		if r.Offset == 4096 {
			t.Errorf("should not have recovered corrupt ZIP at offset 4096 (got %s, %d bytes)", r.Signature.Name, r.Size)
		}
	}
}

// TestCarveWithTypeFilter verifies that the type filter correctly limits output.
func TestCarveWithTypeFilter(t *testing.T) {
	validPDF := mustReadFile(t, "testdata/valid.pdf")
	validZIP := mustReadFile(t, "testdata/valid.zip")

	// Build image with both PDF and ZIP
	const imageSize = 64 * 1024
	image := make([]byte, imageSize)
	rand.Read(image)
	copy(image[4096:], validPDF)
	copy(image[16384:], validZIP)

	imgPath := filepath.Join(t.TempDir(), "mixed.img")
	os.WriteFile(imgPath, image, 0o644)

	// Carve with PDF-only filter
	outputDir := filepath.Join(t.TempDir(), "recovered")
	cfg := Config{
		DiskPath:    imgPath,
		OutputDir:   outputDir,
		BlockSize:   4096,
		TypeFilters: "pdf",
		Verbose:     false,
	}

	c, err := New(cfg)
	if err != nil {
		t.Fatalf("failed to create carver: %v", err)
	}
	defer c.Close()

	c.Scan(nil)
	results := c.Results()

	for _, r := range results {
		if r.Signature.Extension != ".pdf" {
			t.Errorf("type filter 'pdf' should not produce %s files", r.Signature.Extension)
		}
	}
	if len(results) == 0 {
		t.Error("expected at least 1 PDF to be recovered with pdf filter")
	}
}

// TestCarveRecoversPDFAtBlockBoundary tests recovery when a PDF header spans
// a block boundary (tests the overlap buffer logic).
func TestCarveRecoversPDFAtBlockBoundary(t *testing.T) {
	validPDF := mustReadFile(t, "testdata/valid.pdf")

	// Place PDF such that its header starts 2 bytes before the end of a 4096-byte block
	// Header is at offset 4094 (last 2 bytes of block 0, first 2 bytes of block 1)
	const imageSize = 32 * 1024
	image := make([]byte, imageSize)
	// Fill with zeros (not random — avoids accidental signatures)
	offset := 4096 - 2 // 2 bytes before block boundary
	copy(image[offset:], validPDF)

	imgPath := filepath.Join(t.TempDir(), "boundary.img")
	os.WriteFile(imgPath, image, 0o644)

	outputDir := filepath.Join(t.TempDir(), "recovered")
	cfg := Config{
		DiskPath:  imgPath,
		OutputDir: outputDir,
		BlockSize: 4096,
		Verbose:   true,
	}

	c, err := New(cfg)
	if err != nil {
		t.Fatalf("failed to create carver: %v", err)
	}
	defer c.Close()

	c.Scan(nil)
	results := c.Results()

	found := false
	for _, r := range results {
		if r.Signature.Name == "PDF" && r.Offset == int64(offset) {
			found = true
			break
		}
	}
	if !found {
		t.Error("PDF at block boundary (offset 4094) was not recovered")
		for _, r := range results {
			t.Logf("  found: %s at offset %d", r.Signature.Name, r.Offset)
		}
	}
}

// TestCarveMultipleFilesBackToBack tests recovery when files are adjacent
// (no gap between them — simulates tightly packed disk clusters).
func TestCarveMultipleFilesBackToBack(t *testing.T) {
	validPDF := mustReadFile(t, "testdata/valid.pdf")
	validZIP := mustReadFile(t, "testdata/valid.zip")
	multipagePDF := mustReadFile(t, "testdata/multipage.pdf")

	// Pack files back-to-back starting at offset 0
	var image bytes.Buffer
	image.Write(validPDF)
	image.Write(validZIP)
	image.Write(multipagePDF)
	// Pad to minimum size
	for image.Len() < 8192 {
		image.WriteByte(0)
	}

	imgPath := filepath.Join(t.TempDir(), "packed.img")
	os.WriteFile(imgPath, image.Bytes(), 0o644)

	outputDir := filepath.Join(t.TempDir(), "recovered")
	cfg := Config{
		DiskPath:  imgPath,
		OutputDir: outputDir,
		BlockSize: 4096,
		Verbose:   true,
	}

	c, err := New(cfg)
	if err != nil {
		t.Fatalf("failed to create carver: %v", err)
	}
	defer c.Close()

	c.Scan(nil)
	results := c.Results()

	if len(results) < 3 {
		t.Errorf("expected at least 3 files from back-to-back packing, got %d", len(results))
		for _, r := range results {
			t.Logf("  recovered: %s at offset %d (%d bytes)", r.Signature.Name, r.Offset, r.Size)
		}
	}
}

// TestCarveDocxClassification verifies that a recovered ZIP with word/ content
// is correctly classified as DOCX.
func TestCarveDocxClassification(t *testing.T) {
	validDOCX := mustReadFile(t, "testdata/valid.docx")

	const imageSize = 32 * 1024
	image := make([]byte, imageSize)
	copy(image[4096:], validDOCX)

	imgPath := filepath.Join(t.TempDir(), "docx.img")
	os.WriteFile(imgPath, image, 0o644)

	outputDir := filepath.Join(t.TempDir(), "recovered")
	cfg := Config{
		DiskPath:  imgPath,
		OutputDir: outputDir,
		BlockSize: 4096,
		Verbose:   false,
	}

	c, err := New(cfg)
	if err != nil {
		t.Fatalf("failed to create carver: %v", err)
	}
	defer c.Close()

	c.Scan(nil)
	results := c.Results()

	found := false
	for _, r := range results {
		if r.Signature.Name == "DOCX" {
			found = true
			break
		}
	}
	if !found {
		t.Error("DOCX was not classified correctly")
		for _, r := range results {
			t.Logf("  found: %s (%s) at offset %d", r.Signature.Name, r.Signature.Extension, r.Offset)
		}
	}
}

// TestCarveXlsxClassification verifies XLSX classification.
func TestCarveXlsxClassification(t *testing.T) {
	validXLSX := mustReadFile(t, "testdata/valid.xlsx")

	const imageSize = 32 * 1024
	image := make([]byte, imageSize)
	copy(image[4096:], validXLSX)

	imgPath := filepath.Join(t.TempDir(), "xlsx.img")
	os.WriteFile(imgPath, image, 0o644)

	outputDir := filepath.Join(t.TempDir(), "recovered")
	cfg := Config{
		DiskPath:    imgPath,
		OutputDir:   outputDir,
		BlockSize:   4096,
		TypeFilters: "xlsx",
		Verbose:     false,
	}

	c, err := New(cfg)
	if err != nil {
		t.Fatalf("failed to create carver: %v", err)
	}
	defer c.Close()

	c.Scan(nil)
	results := c.Results()

	if len(results) != 1 {
		t.Errorf("expected exactly 1 XLSX, got %d results", len(results))
		for _, r := range results {
			t.Logf("  found: %s (%s)", r.Signature.Name, r.Signature.Extension)
		}
		return
	}
	if results[0].Signature.Name != "XLSX" {
		t.Errorf("expected XLSX, got %s", results[0].Signature.Name)
	}
}

// TestCarvePptxClassification verifies PPTX classification.
func TestCarvePptxClassification(t *testing.T) {
	validPPTX := mustReadFile(t, "testdata/valid.pptx")

	const imageSize = 32 * 1024
	image := make([]byte, imageSize)
	copy(image[4096:], validPPTX)

	imgPath := filepath.Join(t.TempDir(), "pptx.img")
	os.WriteFile(imgPath, image, 0o644)

	outputDir := filepath.Join(t.TempDir(), "recovered")
	cfg := Config{
		DiskPath:    imgPath,
		OutputDir:   outputDir,
		BlockSize:   4096,
		TypeFilters: "pptx",
		Verbose:     false,
	}

	c, err := New(cfg)
	if err != nil {
		t.Fatalf("failed to create carver: %v", err)
	}
	defer c.Close()

	c.Scan(nil)
	results := c.Results()

	if len(results) != 1 {
		t.Errorf("expected exactly 1 PPTX, got %d results", len(results))
		for _, r := range results {
			t.Logf("  found: %s (%s)", r.Signature.Name, r.Signature.Extension)
		}
		return
	}
	if results[0].Signature.Name != "PPTX" {
		t.Errorf("expected PPTX, got %s", results[0].Signature.Name)
	}
}

// TestCarveRejectsFakePKHeader verifies that random data starting with PK\x03\x04
// but with invalid local header fields is rejected.
func TestCarveRejectsFakePKHeader(t *testing.T) {
	const imageSize = 32 * 1024
	image := make([]byte, imageSize)

	// Place a fake PK header with garbage fields at offset 4096
	copy(image[4096:], []byte{0x50, 0x4B, 0x03, 0x04}) // PK signature
	// Version = 0 (invalid), method = 255 (invalid), etc.
	binary.LittleEndian.PutUint16(image[4096+4:], 0)   // version = 0
	binary.LittleEndian.PutUint16(image[4096+8:], 255) // method = 255
	binary.LittleEndian.PutUint16(image[4096+26:], 0)  // nameLen = 0

	imgPath := filepath.Join(t.TempDir(), "fakepk.img")
	os.WriteFile(imgPath, image, 0o644)

	outputDir := filepath.Join(t.TempDir(), "recovered")
	cfg := Config{
		DiskPath:  imgPath,
		OutputDir: outputDir,
		BlockSize: 4096,
		Verbose:   false,
	}

	c, err := New(cfg)
	if err != nil {
		t.Fatalf("failed to create carver: %v", err)
	}
	defer c.Close()

	c.Scan(nil)
	results := c.Results()

	if len(results) > 0 {
		t.Errorf("should not recover anything from fake PK header, got %d results", len(results))
	}
}

// TestValidateRecoveredFileExported tests the exported ValidateRecoveredFile function
// used by NTFS/ext4 recovery paths.
func TestValidateRecoveredFileExported(t *testing.T) {
	validPDF := mustReadFile(t, "testdata/valid.pdf")
	validDOCX := mustReadFile(t, "testdata/valid.docx")
	validXLSX := mustReadFile(t, "testdata/valid.xlsx")
	validZIP := mustReadFile(t, "testdata/valid.zip")

	tests := []struct {
		name string
		data []byte
		ext  string
		want bool
	}{
		{"valid PDF", validPDF, ".pdf", true},
		{"valid DOCX", validDOCX, ".docx", true},
		{"valid XLSX", validXLSX, ".xlsx", true},
		{"valid ZIP", validZIP, ".zip", true},
		{"garbage PDF", append([]byte("%PDF-1.4\n"), bytes.Repeat([]byte{0xAB}, 100)...), ".pdf", false},
		{"empty data", []byte{}, ".pdf", false},
		{"unknown ext passes", []byte("anything"), ".txt", true},
		{"short PDF", []byte("%PDF-1.4 %%EOF"), ".pdf", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ValidateRecoveredFile(tt.data, tt.ext, false)
			if got != tt.want {
				t.Errorf("ValidateRecoveredFile(%s) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

// TestCarveRecoveredFileSizesMatch verifies recovered file sizes match originals.
func TestCarveRecoveredFileSizesMatch(t *testing.T) {
	testFiles := []struct {
		path string
		name string
	}{
		{"testdata/valid.pdf", "PDF"},
		{"testdata/valid.zip", "ZIP"},
		{"testdata/valid.docx", "DOCX"},
	}

	for _, tf := range testFiles {
		t.Run(tf.name, func(t *testing.T) {
			original := mustReadFile(t, tf.path)

			// Build image with file at offset 0
			image := make([]byte, 32*1024)
			copy(image[0:], original)

			imgPath := filepath.Join(t.TempDir(), "size_test.img")
			os.WriteFile(imgPath, image, 0o644)

			outputDir := filepath.Join(t.TempDir(), "recovered")
			c, err := New(Config{
				DiskPath:  imgPath,
				OutputDir: outputDir,
				BlockSize: 4096,
			})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			defer c.Close()

			c.Scan(nil)
			results := c.Results()

			if len(results) == 0 {
				t.Fatalf("no files recovered")
			}

			// The first result at offset 0 should match original size
			for _, r := range results {
				if r.Offset == 0 {
					if r.Size != int64(len(original)) {
						t.Errorf("recovered size %d != original size %d", r.Size, len(original))
					}
					// Read back and compare content
					recovered, err := os.ReadFile(r.OutputPath)
					if err != nil {
						t.Fatalf("failed to read recovered: %v", err)
					}
					if !bytes.Equal(recovered, original) {
						t.Errorf("recovered content differs from original (len recovered=%d, original=%d)",
							len(recovered), len(original))
					}
					return
				}
			}
			t.Error("did not find recovery result at offset 0")
		})
	}
}

// TestCarveDocxFilterRecoversDOCXNotZIP tests that -type docx recovers
// DOCX files but not plain ZIPs.
func TestCarveDocxFilterRecoversDOCXNotZIP(t *testing.T) {
	validDOCX := mustReadFile(t, "testdata/valid.docx")
	validZIP := mustReadFile(t, "testdata/valid.zip")

	const imageSize = 64 * 1024
	image := make([]byte, imageSize)
	copy(image[4096:], validDOCX)
	copy(image[20480:], validZIP)

	imgPath := filepath.Join(t.TempDir(), "filter.img")
	os.WriteFile(imgPath, image, 0o644)

	outputDir := filepath.Join(t.TempDir(), "recovered")
	cfg := Config{
		DiskPath:    imgPath,
		OutputDir:   outputDir,
		BlockSize:   4096,
		TypeFilters: "docx",
		Verbose:     true,
	}

	c, err := New(cfg)
	if err != nil {
		t.Fatalf("failed to create carver: %v", err)
	}
	defer c.Close()

	c.Scan(nil)
	results := c.Results()

	for _, r := range results {
		if r.Signature.Name == "ZIP" {
			t.Errorf("docx filter should not save plain ZIP files")
		}
	}

	docxFound := false
	for _, r := range results {
		if r.Signature.Name == "DOCX" {
			docxFound = true
		}
	}
	if !docxFound {
		t.Error("docx filter should recover DOCX files")
	}
}

// --- Helpers ---

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read %s: %v", path, err)
	}
	return data
}
