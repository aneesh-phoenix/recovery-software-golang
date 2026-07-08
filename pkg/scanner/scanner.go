// Package scanner provides low-level disk/image reading capabilities.
package scanner

import (
	"fmt"
	"io"
	"os"
	"sync"
)

// DiskReader provides sequential and random access to a disk or image file.
type DiskReader struct {
	file     *os.File
	size     int64
	mu       sync.Mutex
}

// NewDiskReader opens a disk device or image file for reading.
func NewDiskReader(path string) (*DiskReader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open %s: %w", path, err)
	}

	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("failed to stat %s: %w", path, err)
	}

	size := info.Size()
	// For block devices, size from Stat() is 0 — seek to end to get size
	if size == 0 {
		size, err = f.Seek(0, io.SeekEnd)
		if err != nil {
			f.Close()
			return nil, fmt.Errorf("failed to determine size of %s: %w", path, err)
		}
		_, _ = f.Seek(0, io.SeekStart)
	}

	return &DiskReader{
		file: f,
		size: size,
	}, nil
}

// Size returns the total size of the disk/image in bytes.
func (d *DiskReader) Size() int64 {
	return d.size
}

// ReadAt reads len(buf) bytes starting at offset.
func (d *DiskReader) ReadAt(buf []byte, offset int64) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.file.ReadAt(buf, offset)
}

// Close closes the underlying file.
func (d *DiskReader) Close() error {
	return d.file.Close()
}

// SampleForZeroBlocks reads a sample of blocks from the disk and returns
// the percentage of blocks that are entirely zeroed (indicating TRIM has run).
// Samples up to 'sampleCount' blocks of 'blockSize' bytes at random offsets.
func (d *DiskReader) SampleForZeroBlocks(blockSize int, sampleCount int) float64 {
	if d.size == 0 || blockSize <= 0 || sampleCount <= 0 {
		return 0.0
	}

	totalBlocks := d.size / int64(blockSize)
	if totalBlocks == 0 {
		return 0.0
	}

	// Limit sample count to available blocks
	if int64(sampleCount) > totalBlocks {
		sampleCount = int(totalBlocks)
	}

	// Use evenly spaced offsets across the disk
	step := totalBlocks / int64(sampleCount)
	if step == 0 {
		step = 1
	}

	buf := make([]byte, blockSize)
	zeroCount := 0
	sampled := 0

	for i := 0; i < sampleCount; i++ {
		blockIdx := int64(i) * step
		offset := blockIdx * int64(blockSize)
		if offset+int64(blockSize) > d.size {
			break
		}

		n, err := d.ReadAt(buf, offset)
		if err != nil || n != blockSize {
			continue
		}

		sampled++
		allZero := true
		for _, b := range buf[:n] {
			if b != 0 {
				allZero = false
				break
			}
		}
		if allZero {
			zeroCount++
		}
	}

	if sampled == 0 {
		return 0.0
	}
	return float64(zeroCount) / float64(sampled) * 100.0
}

// ScanBlocks iterates over the disk in blocks of the given size,
// calling the callback with offset and block data.
// The callback should return true to continue scanning, false to stop.
type BlockCallback func(offset int64, data []byte) bool

func (d *DiskReader) ScanBlocks(blockSize int, callback BlockCallback) error {
	buf := make([]byte, blockSize)
	var offset int64

	for offset < d.size {
		n, err := d.ReadAt(buf, offset)
		if n > 0 {
			if !callback(offset, buf[:n]) {
				return nil
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read error at offset %d: %w", offset, err)
		}
		offset += int64(n)
	}
	return nil
}
