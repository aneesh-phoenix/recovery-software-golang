// Package carver implements file carving from raw disk data.
package carver

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
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
	reader    *scanner.DiskReader
	outputDir string
	blockSize int
	results   []Result
	mu        sync.Mutex
	counter   atomic.Int64
	verbose   bool
}

// Config holds carver configuration.
type Config struct {
	DiskPath  string
	OutputDir string
	BlockSize int
	Verbose   bool
}

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

	return &Carver{
		reader:    reader,
		outputDir: cfg.OutputDir,
		blockSize: blockSize,
		verbose:   cfg.Verbose,
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
	signatures := signature.Registry

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
				if sig.MatchHeader(searchBuf[i:]) {
					actualOffset := searchOffset + int64(i)
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
	// Read up to max file size to find the footer
	readSize := sig.MaxSize
	remaining := c.reader.Size() - offset
	if readSize > remaining {
		readSize = remaining
	}

	// Cap read size for efficiency — read in chunks
	const chunkSize = 1024 * 1024 // 1MB chunks
	var fileData []byte

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

		// Check if we've found the footer in accumulated data
		if len(sig.Footer) > 0 {
			footerEnd := sig.FindFooter(fileData)
			if footerEnd > 0 {
				fileData = fileData[:footerEnd]
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

	if len(fileData) < len(sig.Header)+10 {
		return // Too small to be valid
	}

	// For ZIP-based files, try to distinguish DOCX from regular ZIP
	actualSig := sig
	if sig.Extension == ".zip" || sig.Extension == ".docx" {
		actualSig = c.classifyZip(fileData, sig)
	}

	// Save the file
	count := c.counter.Add(1)
	filename := fmt.Sprintf("recovered_%04d_%s%s", count, actualSig.Name, actualSig.Extension)
	outPath := filepath.Join(c.outputDir, filename)

	if err := os.WriteFile(outPath, fileData, 0o644); err != nil {
		if c.verbose {
			fmt.Printf("[-] Failed to write %s: %v\n", outPath, err)
		}
		return
	}

	result := Result{
		Signature:  actualSig,
		Offset:     offset,
		Size:       int64(len(fileData)),
		OutputPath: outPath,
	}

	c.mu.Lock()
	c.results = append(c.results, result)
	c.mu.Unlock()

	if c.verbose {
		fmt.Printf("[✓] Recovered %s (%d bytes) → %s\n", actualSig.Name, len(fileData), outPath)
	}
}

// classifyZip attempts to determine if a ZIP file is actually a DOCX/XLSX/PPTX.
func (c *Carver) classifyZip(data []byte, fallback *signature.FileSignature) *signature.FileSignature {
	// Look for Office-specific markers in the ZIP content
	officeMarkers := []struct {
		marker []byte
		name   string
		ext    string
	}{
		{[]byte("word/"), "DOCX", ".docx"},
		{[]byte("[Content_Types].xml"), "DOCX", ".docx"},
		{[]byte("xl/"), "XLSX", ".xlsx"},
		{[]byte("ppt/"), "PPTX", ".pptx"},
	}

	// Search first 4KB of the file for markers
	searchLen := 4096
	if len(data) < searchLen {
		searchLen = len(data)
	}

	for _, m := range officeMarkers {
		for i := 0; i <= searchLen-len(m.marker); i++ {
			match := true
			for j := range m.marker {
				if data[i+j] != m.marker[j] {
					match = false
					break
				}
			}
			if match {
				return &signature.FileSignature{
					Name:      m.name,
					Extension: m.ext,
					Header:    fallback.Header,
					Footer:    fallback.Footer,
					MaxSize:   fallback.MaxSize,
				}
			}
		}
	}

	// Default to ZIP
	return &signature.FileSignature{
		Name:      "ZIP",
		Extension: ".zip",
		Header:    fallback.Header,
		Footer:    fallback.Footer,
		MaxSize:   fallback.MaxSize,
	}
}
