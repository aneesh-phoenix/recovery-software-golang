// Package carver implements file carving from raw disk data.
package carver

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/aneesh/recovery-soft/pkg/scanner"
	"github.com/aneesh/recovery-soft/pkg/signature"
)

// Result represents a single recovered file.
type Result struct {
	Signature  *signature.FileSignature
	Offset     int64
	Size       int64
	OutputPath string
	Warnings   []string
}

// Carver performs file carving on a disk or image.
type Carver struct {
	reader          *scanner.DiskReader
	outputDir       string
	blockSize       int
	signatures      []signature.FileSignature
	outputTypes     map[string]bool
	results         []Result
	recoveredRanges []byteRange
	warnings        []string
	mu              sync.Mutex
	counter         atomic.Int64
	verbose         bool
}

// Config holds carver configuration.
type Config struct {
	DiskPath    string
	OutputDir   string
	BlockSize   int
	Signatures  []signature.FileSignature
	TypeFilters string
	Verbose     bool
}

type byteRange struct {
	start int64
	end   int64
}

const (
	zipLocalFileHeaderSignature  = 0x04034b50
	zipCentralDirectorySignature = 0x02014b50
	zipEOCDSignature             = 0x06054b50
	minEOCDRecordSize            = 22
	maxZIPCommentSize            = 65535
	maxEOCDRecordSize            = minEOCDRecordSize + maxZIPCommentSize
)

// New creates a new Carver instance.
func New(cfg Config) (*Carver, error) {
	reader, err := scanner.NewDiskReader(cfg.DiskPath)
	if err != nil {
		return nil, err
	}

	if err := os.MkdirAll(cfg.OutputDir, 0o755); err != nil {
		reader.Close()
		return nil, fmt.Errorf("failed to create output directory: %w", err)
	}

	blockSize := cfg.BlockSize
	if blockSize <= 0 {
		blockSize = 4096 // Default 4KB blocks (common cluster size)
	}

	signatures := cfg.Signatures
	if len(signatures) == 0 {
		signatures, err = signature.ForTypes(cfg.TypeFilters)
		if err != nil {
			reader.Close()
			return nil, err
		}
	}
	outputTypes, err := parseOutputTypeFilter(cfg.TypeFilters)
	if err != nil {
		reader.Close()
		return nil, err
	}

	return &Carver{
		reader:      reader,
		outputDir:   cfg.OutputDir,
		blockSize:   blockSize,
		signatures:  signatures,
		outputTypes: outputTypes,
		verbose:     cfg.Verbose,
	}, nil
}

// Close releases resources.
func (c *Carver) Close() error {
	return c.reader.Close()
}

// Results returns all recovered files.
func (c *Carver) Results() []Result {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Result{}, c.results...)
}

// Warnings returns all fragmentation/corruption warnings collected during scanning.
func (c *Carver) Warnings() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string{}, c.warnings...)
}

func (c *Carver) addWarning(msg string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.warnings = append(c.warnings, msg)
}

// DiskSize returns the total disk/image size.
func (c *Carver) DiskSize() int64 {
	return c.reader.Size()
}

// Reader returns the underlying DiskReader for additional analysis (e.g., TRIM detection).
func (c *Carver) Reader() *scanner.DiskReader {
	return c.reader
}

// ValidateRecoveredFile checks whether the recovered data is a valid file of
// the detected type. Returns true if the file passes validation, false if it
// looks corrupt or is a false positive. This is intended for NTFS/ext4 recovery
// paths where files are recovered by filesystem metadata rather than carving.
func ValidateRecoveredFile(data []byte, extension string, verbose bool) bool {
	ext := strings.ToLower(extension)
	switch ext {
	case ".pdf":
		return validatePDF(data, verbose, 0)
	case ".zip", ".docx", ".xlsx", ".pptx":
		ok, err := validateZip(data)
		if !ok && verbose {
			fmt.Printf("[-] ZIP validation failed: %v\n", err)
		}
		return ok
	default:
		// No validator for this type — accept it
		return true
	}
}

// Scan performs the file carving operation.
func (c *Carver) Scan(progressFn func(offset, total int64)) error {
	totalSize := c.reader.Size()
	signatures := c.signatures

	// Use a sliding window to detect headers that span block boundaries
	overlapSize := 64 // Enough to catch any header
	prevTail := make([]byte, 0, overlapSize)

	err := c.reader.ScanBlocks(c.blockSize, func(offset int64, data []byte) bool {
		if progressFn != nil {
			progressFn(offset, totalSize)
		}

		// Combine previous block tail with current block head for boundary detection
		var searchBuf []byte
		var searchOffset int64
		if len(prevTail) > 0 {
			searchBuf = append(prevTail, data...)
			searchOffset = offset - int64(len(prevTail))
		} else {
			searchBuf = data
			searchOffset = offset
		}

		// Search for signatures in the combined buffer
		for i := range searchBuf {
			for sigIdx := range signatures {
				sig := &signatures[sigIdx]
				actualOffset := searchOffset + int64(i)
				if actualOffset < offset && actualOffset+int64(len(sig.Header)) <= offset {
					continue
				}
				if sig.MatchHeader(searchBuf[i:]) {
					if sig.Extension == ".zip" && c.isOffsetRecovered(actualOffset) {
						if c.verbose {
							fmt.Printf("[-] Ignoring %s header inside recovered range at offset %d (0x%X)\n", sig.Name, actualOffset, actualOffset)
						}
						continue
					}
					if c.verbose {
						fmt.Printf("[+] Found %s header at offset %d (0x%X)\n", sig.Name, actualOffset, actualOffset)
					}
					c.extractFile(sig, actualOffset)
				}
			}
		}

		// Save tail for next iteration
		if len(data) >= overlapSize {
			prevTail = append(prevTail[:0], data[len(data)-overlapSize:]...)
		} else {
			prevTail = append(prevTail[:0], data...)
		}

		return true
	})

	return err
}

// extractFile reads from the detected header offset and saves the file.
func (c *Carver) extractFile(sig *signature.FileSignature, offset int64) {
	if sig.Extension == ".zip" {
		c.extractZip(sig, offset)
		return
	}

	// Read up to max file size to find the footer
	readSize := sig.MaxSize
	remaining := c.reader.Size() - offset
	if readSize > remaining {
		readSize = remaining
	}

	// For PDF, use a more conservative initial read limit. Most PDFs are under
	// 100MB; reading 500MB looking for %%EOF picks up garbage on disk.
	// We still allow up to MaxSize if needed, but search smarter.
	isPDF := sig.Extension == ".pdf"
	if isPDF {
		const pdfInitialLimit = 100 * 1024 * 1024 // 100MB
		if readSize > pdfInitialLimit {
			readSize = pdfInitialLimit
		}
	}

	// Cap read size for efficiency — read in chunks
	const chunkSize = 1024 * 1024 // 1MB chunks
	var fileData []byte
	foundFooter := len(sig.Footer) == 0
	searchStart := 0

	for readOffset := int64(0); readOffset < readSize; readOffset += chunkSize {
		size := chunkSize
		if readOffset+int64(size) > readSize {
			size = int(readSize - readOffset)
		}

		chunk := make([]byte, size)
		n, err := c.reader.ReadAt(chunk, offset+readOffset)
		if n == 0 {
			break
		}
		oldLen := len(fileData)
		fileData = append(fileData, chunk[:n]...)

		// Search only the newly added range plus enough overlap for a split footer.
		if len(sig.Footer) > 0 {
			searchStart = oldLen - len(sig.Footer) + 1
			if searchStart < 0 {
				searchStart = 0
			}

			if isPDF {
				// Use PDF-aware footer search that validates startxref precedes %%EOF
				pdfEnd := findPDFEnd(fileData[searchStart:])
				if pdfEnd >= 0 {
					fileData = fileData[:searchStart+pdfEnd]
					foundFooter = true
					break
				}
			} else {
				footerEnd := findFooterEnd(fileData, sig.Footer, searchStart)
				if footerEnd >= 0 {
					fileData = fileData[:footerEnd]
					foundFooter = true
					break
				}
			}
		}

		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}
	}

	if !foundFooter {
		if c.verbose {
			fmt.Printf("[-] Rejected %s candidate without footer at offset %d (0x%X)\n", sig.Name, offset, offset)
		}
		return
	}
	if len(fileData) < len(sig.Header)+10 {
		return // Too small to be valid
	}

	// Validate PDF structure before saving
	if isPDF && !validatePDF(fileData, c.verbose, offset) {
		// Check for possible fragmentation: has some valid PDF markers but failed full validation
		if bytes.Contains(fileData, []byte(" obj")) && (bytes.Contains(fileData, []byte("stream")) || bytes.Contains(fileData, []byte("/Page"))) {
			warning := fmt.Sprintf("[!] Possible fragmented file at offset %d (0x%X): passed header check but failed full validation", offset, offset)
			c.addWarning(warning)
			if c.verbose {
				fmt.Println(warning)
			}
		}
		return
	}

	if !c.shouldSave(sig) {
		return
	}
	c.saveFile(sig, offset, fileData)
}

func (c *Carver) extractZip(sig *signature.FileSignature, offset int64) {
	readSize := sig.MaxSize
	remaining := c.reader.Size() - offset
	if readSize > remaining {
		readSize = remaining
	}

	const chunkSize = 1024 * 1024 // 1MB chunks
	var fileData []byte
	lastChecked := 0

	for readOffset := int64(0); readOffset < readSize; readOffset += chunkSize {
		size := chunkSize
		if readOffset+int64(size) > readSize {
			size = int(readSize - readOffset)
		}

		chunk := make([]byte, size)
		n, err := c.reader.ReadAt(chunk, offset+readOffset)
		if n == 0 {
			break
		}
		fileData = append(fileData, chunk[:n]...)

		if archiveEnd, ok := findValidZipEnd(fileData, lastChecked); ok {
			fileData = fileData[:archiveEnd]
			break
		}
		if len(fileData) > maxEOCDRecordSize {
			lastChecked = len(fileData) - maxEOCDRecordSize
		}

		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}
	}

	if ok, _ := validateZip(fileData); !ok {
		// Check for possible fragmentation: valid local file header but failed full validation
		if len(fileData) >= 30 && validateFirstLocalHeader(fileData) == nil {
			warning := fmt.Sprintf("[!] Possible fragmented file at offset %d (0x%X): passed header check but failed full validation", offset, offset)
			c.addWarning(warning)
			if c.verbose {
				fmt.Println(warning)
			}
		} else if c.verbose {
			fmt.Printf("[-] Rejected invalid ZIP candidate at offset %d (0x%X)\n", offset, offset)
		}
		return
	}

	actualSig := c.classifyZip(fileData, sig)
	c.addRecoveredRange(offset, offset+int64(len(fileData)))
	if !c.shouldSave(actualSig) {
		if c.verbose {
			fmt.Printf("[-] Skipping %s at offset %d due to type filter\n", actualSig.Name, offset)
		}
		return
	}
	c.saveFile(actualSig, offset, fileData)
}

func (c *Carver) shouldSave(sig *signature.FileSignature) bool {
	if len(c.outputTypes) == 0 {
		return true
	}
	return c.outputTypes[strings.TrimPrefix(strings.ToLower(sig.Extension), ".")]
}

func parseOutputTypeFilter(typeList string) (map[string]bool, error) {
	if strings.TrimSpace(typeList) == "" {
		return nil, nil
	}

	supported := map[string]bool{
		"pdf":  true,
		"zip":  true,
		"docx": true,
		"xlsx": true,
		"pptx": true,
	}
	selected := make(map[string]bool)
	for _, part := range strings.Split(typeList, ",") {
		key := strings.ToLower(strings.TrimSpace(part))
		if key == "" {
			continue
		}
		if !supported[key] {
			return nil, fmt.Errorf("unsupported file type %q (supported: pdf, zip, docx, xlsx, pptx)", key)
		}
		selected[key] = true
	}
	if len(selected) == 0 {
		return nil, fmt.Errorf("no file types selected")
	}
	return selected, nil
}

func findFooterEnd(data, footer []byte, start int) int {
	if len(footer) == 0 {
		return len(data)
	}
	if start < 0 {
		start = 0
	}
	if start >= len(data) {
		return -1
	}
	idx := bytes.Index(data[start:], footer)
	if idx < 0 {
		return -1
	}
	return start + idx + len(footer)
}

// findPDFEnd locates the true end of a PDF file by searching for %%EOF that
// is preceded by a valid startxref reference (within 100 bytes before it).
// This avoids matching stray %%EOF sequences in random disk data.
// Falls back to the first valid %%EOF if no startxref-preceded one is found.
func findPDFEnd(data []byte) int {
	eofMarker := []byte("%%EOF")
	startxrefMarker := []byte("startxref")
	searchFrom := 0
	firstEOF := -1

	for {
		idx := bytes.Index(data[searchFrom:], eofMarker)
		if idx < 0 {
			break
		}
		eofPos := searchFrom + idx
		endPos := eofPos + len(eofMarker)

		// Consume trailing newline(s) after %%EOF (part of valid PDF termination)
		for endPos < len(data) && (data[endPos] == '\n' || data[endPos] == '\r') {
			endPos++
		}

		if firstEOF < 0 {
			firstEOF = endPos
		}

		// Check if startxref appears within 100 bytes before this %%EOF
		// A valid PDF always has: startxref\nNNNNN\n%%EOF
		lookback := 100
		start := eofPos - lookback
		if start < 0 {
			start = 0
		}
		region := data[start:eofPos]
		if bytes.Contains(region, startxrefMarker) {
			return endPos
		}

		searchFrom = eofPos + len(eofMarker)
	}

	// Fallback: return first %%EOF even without startxref (might be a minimal/damaged PDF)
	return firstEOF
}

// validatePDF performs structural validation on a candidate PDF to reject false
// positives. A valid PDF must have:
// 1. A proper header (%PDF-x.y)
// 2. At least one object definition (N N obj)
// 3. A cross-reference section (xref or startxref)
// 4. A trailer or xref stream
// Files that are just random data between %PDF and %%EOF are rejected.
func validatePDF(data []byte, verbose bool, offset int64) bool {
	if len(data) < 67 { // Minimum plausible PDF: header + one obj + xref + trailer + eof
		if verbose {
			fmt.Printf("[-] Rejected PDF at offset %d: too small (%d bytes)\n", offset, len(data))
		}
		return false
	}

	// Check for a proper PDF version header: %PDF-N.N
	if len(data) < 8 || data[4] != '-' {
		if verbose {
			fmt.Printf("[-] Rejected PDF at offset %d: malformed header\n", offset)
		}
		return false
	}
	// Version digit check (e.g., %PDF-1.4, %PDF-2.0)
	if data[5] < '0' || data[5] > '9' || data[6] != '.' || data[7] < '0' || data[7] > '9' {
		if verbose {
			fmt.Printf("[-] Rejected PDF at offset %d: invalid version in header\n", offset)
		}
		return false
	}

	// Must contain at least one object definition: "N N obj" pattern
	// We look for common markers rather than full regex for speed.
	hasObj := bytes.Contains(data, []byte(" obj")) || bytes.Contains(data, []byte("\nobj"))
	if !hasObj {
		if verbose {
			fmt.Printf("[-] Rejected PDF at offset %d: no object definitions found\n", offset)
		}
		return false
	}

	// Must contain cross-reference information (xref table or startxref pointer)
	hasXref := bytes.Contains(data, []byte("startxref")) || bytes.Contains(data, []byte("xref"))
	if !hasXref {
		if verbose {
			fmt.Printf("[-] Rejected PDF at offset %d: no cross-reference section\n", offset)
		}
		return false
	}

	// Must contain "trailer" or a cross-reference stream (linearized PDFs may
	// use xref streams identified by /Type /XRef in the object).
	hasTrailer := bytes.Contains(data, []byte("trailer")) || bytes.Contains(data, []byte("/Type /XRef")) || bytes.Contains(data, []byte("/Type/XRef"))
	if !hasTrailer {
		if verbose {
			fmt.Printf("[-] Rejected PDF at offset %d: no trailer or xref stream\n", offset)
		}
		return false
	}

	// Sanity check: ratio of non-printable/non-whitespace bytes in the first 1KB
	// should not be overwhelming. Real PDFs have text-based structure at the start.
	checkLen := 1024
	if checkLen > len(data) {
		checkLen = len(data)
	}
	binaryCount := 0
	for i := 0; i < checkLen; i++ {
		b := data[i]
		// Allow printable ASCII, whitespace, and high bytes (binary streams in PDF are normal)
		if b < 0x09 || (b > 0x0D && b < 0x20 && b != 0x1B) {
			binaryCount++
		}
	}
	// If more than 30% of the first KB is control characters, likely not a real PDF header region
	if binaryCount > checkLen*30/100 {
		if verbose {
			fmt.Printf("[-] Rejected PDF at offset %d: header region has too many control bytes (%d/%d)\n", offset, binaryCount, checkLen)
		}
		return false
	}

	return true
}

func (c *Carver) saveFile(sig *signature.FileSignature, offset int64, data []byte) {
	count := c.counter.Add(1)
	filename := fmt.Sprintf("recovered_%04d_%s%s", count, sig.Name, sig.Extension)
	outPath := filepath.Join(c.outputDir, filename)

	if err := os.WriteFile(outPath, data, 0o644); err != nil {
		if c.verbose {
			fmt.Printf("[-] Failed to write %s: %v\n", outPath, err)
		}
		return
	}

	result := Result{
		Signature:  sig,
		Offset:     offset,
		Size:       int64(len(data)),
		OutputPath: outPath,
	}

	c.mu.Lock()
	c.results = append(c.results, result)
	c.mu.Unlock()

	if c.verbose {
		fmt.Printf("[✓] Recovered %s (%d bytes) → %s\n", sig.Name, len(data), outPath)
	}
}

func (c *Carver) isOffsetRecovered(offset int64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, r := range c.recoveredRanges {
		if offset >= r.start && offset < r.end {
			return true
		}
	}
	return false
}

func (c *Carver) addRecoveredRange(start, end int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.recoveredRanges = append(c.recoveredRanges, byteRange{start: start, end: end})
}

// classifyZip attempts to determine if a ZIP file is actually a DOCX/XLSX/PPTX.
// Uses both directory path prefixes and [Content_Types].xml for classification.
func (c *Carver) classifyZip(data []byte, fallback *signature.FileSignature) *signature.FileSignature {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return zipSignature("ZIP", ".zip", fallback)
	}

	hasContentTypes := false
	hasWord := false
	hasXL := false
	hasPPT := false

	for _, f := range zr.File {
		name := f.Name
		switch {
		case name == "[Content_Types].xml":
			hasContentTypes = true
		case strings.HasPrefix(name, "word/"):
			hasWord = true
		case strings.HasPrefix(name, "xl/"):
			hasXL = true
		case strings.HasPrefix(name, "ppt/"):
			hasPPT = true
		}
	}

	// Office Open XML files always have [Content_Types].xml at the root.
	// If it's missing but we have directory prefixes, still classify but
	// it's a weaker signal.
	if hasWord && hasContentTypes {
		return zipSignature("DOCX", ".docx", fallback)
	}
	if hasXL && hasContentTypes {
		return zipSignature("XLSX", ".xlsx", fallback)
	}
	if hasPPT && hasContentTypes {
		return zipSignature("PPTX", ".pptx", fallback)
	}
	// Fallback to directory-only classification
	if hasWord {
		return zipSignature("DOCX", ".docx", fallback)
	}
	if hasXL {
		return zipSignature("XLSX", ".xlsx", fallback)
	}
	if hasPPT {
		return zipSignature("PPTX", ".pptx", fallback)
	}

	return zipSignature("ZIP", ".zip", fallback)
}

func zipSignature(name, ext string, fallback *signature.FileSignature) *signature.FileSignature {
	return &signature.FileSignature{
		Name:      name,
		Extension: ext,
		Header:    fallback.Header,
		Footer:    fallback.Footer,
		MaxSize:   fallback.MaxSize,
	}
}

func findValidZipEnd(data []byte, start int) (int, bool) {
	if start < 0 {
		start = 0
	}
	if len(data) < minEOCDRecordSize {
		return 0, false
	}
	limit := len(data) - minEOCDRecordSize
	for i := start; i <= limit; i++ {
		if binary.LittleEndian.Uint32(data[i:i+4]) != zipEOCDSignature {
			continue
		}
		commentLen := int(binary.LittleEndian.Uint16(data[i+20 : i+22]))
		archiveEnd := i + minEOCDRecordSize + commentLen
		if archiveEnd > len(data) {
			continue
		}
		if ok, _ := validateZip(data[:archiveEnd]); ok {
			return archiveEnd, true
		}
	}
	return 0, false
}

func validateZip(data []byte) (bool, error) {
	if len(data) < minEOCDRecordSize {
		return false, errors.New("zip candidate is smaller than EOCD")
	}
	if binary.LittleEndian.Uint32(data[:4]) != zipLocalFileHeaderSignature {
		return false, errors.New("missing local file header")
	}

	// Validate first local file header fields for sanity
	if err := validateFirstLocalHeader(data); err != nil {
		return false, fmt.Errorf("bad local header: %w", err)
	}

	eocdOffset, ok := findEOCDAtEnd(data)
	if !ok {
		return false, errors.New("missing end of central directory")
	}
	if eocdOffset+minEOCDRecordSize > len(data) {
		return false, errors.New("truncated end of central directory")
	}

	diskNumber := binary.LittleEndian.Uint16(data[eocdOffset+4 : eocdOffset+6])
	cdStartDisk := binary.LittleEndian.Uint16(data[eocdOffset+6 : eocdOffset+8])
	if diskNumber != 0 || cdStartDisk != 0 {
		return false, errors.New("multi-disk ZIP archives are not supported")
	}

	totalEntries := int(binary.LittleEndian.Uint16(data[eocdOffset+10 : eocdOffset+12]))
	centralDirSize := int64(binary.LittleEndian.Uint32(data[eocdOffset+12 : eocdOffset+16]))
	centralDirOffset := int64(binary.LittleEndian.Uint32(data[eocdOffset+16 : eocdOffset+20]))
	if totalEntries == 0 {
		return false, errors.New("empty ZIP archive")
	}
	if centralDirSize <= 0 || centralDirOffset <= 0 {
		return false, errors.New("invalid central directory offsets")
	}
	if centralDirOffset+centralDirSize != int64(eocdOffset) {
		return false, errors.New("central directory does not end at EOCD")
	}
	if centralDirOffset+centralDirSize > int64(len(data)) {
		return false, errors.New("central directory extends beyond archive")
	}

	if err := validateCentralDirectory(data, int(centralDirOffset), int(centralDirSize), totalEntries); err != nil {
		return false, err
	}

	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return false, err
	}
	if len(zr.File) == 0 {
		return false, errors.New("empty ZIP archive")
	}

	// Attempt decompression of the first small file to verify data integrity.
	// This catches structurally valid ZIPs with corrupted compressed data.
	if err := validateZipDecompression(zr); err != nil {
		return false, fmt.Errorf("decompression check failed: %w", err)
	}

	return true, nil
}

// validateFirstLocalHeader checks the first local file header for sane field values.
// This rejects false positives where random data happens to start with PK\x03\x04.
func validateFirstLocalHeader(data []byte) error {
	if len(data) < 30 {
		return errors.New("too short for local file header")
	}

	// Version needed to extract (offset 4, 2 bytes)
	version := binary.LittleEndian.Uint16(data[4:6])
	// Version should be reasonable: 10 (1.0) to 63 (6.3)
	if version == 0 || version > 63 {
		return fmt.Errorf("implausible version needed: %d", version)
	}

	// Compression method (offset 8, 2 bytes)
	method := binary.LittleEndian.Uint16(data[8:10])
	// Valid methods: 0 (stored), 1-6 (legacy), 8 (deflate), 9 (deflate64),
	// 12 (bzip2), 14 (LZMA), 93 (Zstandard), 95 (XZ), 98 (PPMd)
	validMethods := map[uint16]bool{
		0: true, 1: true, 2: true, 3: true, 4: true, 5: true, 6: true,
		8: true, 9: true, 10: true, 12: true, 14: true, 18: true, 19: true,
		93: true, 95: true, 98: true,
	}
	if !validMethods[method] {
		return fmt.Errorf("invalid compression method: %d", method)
	}

	// File name length (offset 26, 2 bytes) — must be > 0 and reasonable
	nameLen := binary.LittleEndian.Uint16(data[26:28])
	if nameLen == 0 {
		return errors.New("empty file name in first entry")
	}
	if nameLen > 512 {
		return fmt.Errorf("implausible file name length: %d", nameLen)
	}

	// Extra field length (offset 28, 2 bytes) — must be reasonable
	extraLen := binary.LittleEndian.Uint16(data[28:30])
	if extraLen > 65535 {
		return fmt.Errorf("implausible extra field length: %d", extraLen)
	}

	// Verify we have enough data for the file name and check it's printable
	nameEnd := 30 + int(nameLen)
	if nameEnd > len(data) {
		return errors.New("file name extends beyond data")
	}
	fileName := data[30:nameEnd]
	for _, b := range fileName {
		// Allow printable ASCII (0x20-0x7E), forward slash, and UTF-8 continuation bytes
		if b < 0x20 && b != 0x09 {
			return fmt.Errorf("non-printable byte 0x%02X in file name", b)
		}
	}

	return nil
}

// validateZipDecompression attempts to open and read at least one file from the
// ZIP archive to verify the compressed data isn't corrupted. Only tests small
// files (under 1MB uncompressed) to avoid excessive memory use.
func validateZipDecompression(zr *zip.Reader) error {
	const maxTestSize = 1024 * 1024 // Only test files under 1MB

	for _, f := range zr.File {
		// Skip directories
		if strings.HasSuffix(f.Name, "/") {
			continue
		}
		// Skip large files — testing them is too expensive
		if f.UncompressedSize64 > maxTestSize {
			continue
		}
		// Skip files with zero size (they're valid but don't test compression)
		if f.UncompressedSize64 == 0 {
			continue
		}

		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("cannot open %q: %w", f.Name, err)
		}
		// Read and discard — this exercises decompression + CRC verification
		buf := make([]byte, 4096)
		totalRead := int64(0)
		for {
			n, readErr := rc.Read(buf)
			totalRead += int64(n)
			if readErr == io.EOF {
				break
			}
			if readErr != nil {
				rc.Close()
				return fmt.Errorf("decompression error in %q: %w", f.Name, readErr)
			}
			// Safety: don't read more than expected
			if totalRead > int64(f.UncompressedSize64)+4096 {
				rc.Close()
				return fmt.Errorf("file %q exceeds declared size", f.Name)
			}
		}
		rc.Close()
		// Successfully decompressed one file — archive is likely valid
		return nil
	}

	// No testable files found — pass validation (structure is already verified)
	return nil
}

func findEOCDAtEnd(data []byte) (int, bool) {
	minOffset := len(data) - maxEOCDRecordSize
	if minOffset < 0 {
		minOffset = 0
	}
	for i := len(data) - minEOCDRecordSize; i >= minOffset; i-- {
		if binary.LittleEndian.Uint32(data[i:i+4]) != zipEOCDSignature {
			continue
		}
		commentLen := int(binary.LittleEndian.Uint16(data[i+20 : i+22]))
		if i+minEOCDRecordSize+commentLen == len(data) {
			return i, true
		}
	}
	return 0, false
}

func validateCentralDirectory(data []byte, offset, size, totalEntries int) error {
	end := offset + size
	if offset < 0 || size < 0 || end > len(data) || offset >= end {
		return errors.New("central directory is outside archive bounds")
	}

	pos := offset
	for entry := 0; entry < totalEntries; entry++ {
		if pos+46 > end {
			return errors.New("truncated central directory entry")
		}
		if binary.LittleEndian.Uint32(data[pos:pos+4]) != zipCentralDirectorySignature {
			return errors.New("invalid central directory header")
		}
		nameLen := int(binary.LittleEndian.Uint16(data[pos+28 : pos+30]))
		extraLen := int(binary.LittleEndian.Uint16(data[pos+30 : pos+32]))
		commentLen := int(binary.LittleEndian.Uint16(data[pos+32 : pos+34]))
		localHeaderOffset := int64(binary.LittleEndian.Uint32(data[pos+42 : pos+46]))
		if localHeaderOffset < 0 || localHeaderOffset+4 > int64(offset) {
			return errors.New("invalid local header offset")
		}
		if binary.LittleEndian.Uint32(data[localHeaderOffset:localHeaderOffset+4]) != zipLocalFileHeaderSignature {
			return errors.New("central directory points to a missing local header")
		}

		pos += 46 + nameLen + extraLen + commentLen
		if pos > end {
			return errors.New("central directory entry exceeds bounds")
		}
	}
	if pos != end {
		return errors.New("central directory has trailing bytes")
	}
	return nil
}
