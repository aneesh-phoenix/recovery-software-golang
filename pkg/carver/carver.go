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

// DiskSize returns the total disk/image size.
func (c *Carver) DiskSize() int64 {
	return c.reader.Size()
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
			footerEnd := findFooterEnd(fileData, sig.Footer, searchStart)
			if footerEnd >= 0 {
				fileData = fileData[:footerEnd]
				foundFooter = true
				break
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
		if c.verbose {
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
func (c *Carver) classifyZip(data []byte, fallback *signature.FileSignature) *signature.FileSignature {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return zipSignature("ZIP", ".zip", fallback)
	}

	for _, f := range zr.File {
		switch {
		case strings.HasPrefix(f.Name, "word/"):
			return zipSignature("DOCX", ".docx", fallback)
		case strings.HasPrefix(f.Name, "xl/"):
			return zipSignature("XLSX", ".xlsx", fallback)
		case strings.HasPrefix(f.Name, "ppt/"):
			return zipSignature("PPTX", ".pptx", fallback)
		}
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
	return true, nil
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
