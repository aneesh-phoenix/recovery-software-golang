package scanner

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// helper to create a temp file with given content and return its path.
func createTempFile(t *testing.T, data []byte) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "testfile.bin")
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	return path
}

// --- 1. NewDiskReader with valid file ---

func TestNewDiskReader_ValidFile(t *testing.T) {
	data := []byte("hello world recovery test data")
	path := createTempFile(t, data)

	reader, err := NewDiskReader(path)
	if err != nil {
		t.Fatalf("NewDiskReader returned error for valid file: %v", err)
	}
	defer reader.Close()

	if reader.file == nil {
		t.Fatal("expected non-nil file handle")
	}
	if reader.size != int64(len(data)) {
		t.Errorf("expected size %d, got %d", len(data), reader.size)
	}
}

// --- 2. NewDiskReader with nonexistent file ---

func TestNewDiskReader_NonexistentFile(t *testing.T) {
	reader, err := NewDiskReader("/tmp/this_file_does_not_exist_12345.bin")
	if err == nil {
		reader.Close()
		t.Fatal("expected error for nonexistent file, got nil")
	}
	if reader != nil {
		t.Fatal("expected nil reader for nonexistent file")
	}
}

// --- 3. Size() returns correct file size ---

func TestSize_ReturnsCorrectSize(t *testing.T) {
	tests := []struct {
		name string
		data []byte
	}{
		{"empty", []byte{}},
		{"one byte", []byte{0x42}},
		{"1KB", make([]byte, 1024)},
		{"mixed content", []byte("PDF recovery tool test content with various bytes")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := createTempFile(t, tt.data)
			reader, err := NewDiskReader(path)
			if err != nil {
				t.Fatalf("NewDiskReader error: %v", err)
			}
			defer reader.Close()

			if got := reader.Size(); got != int64(len(tt.data)) {
				t.Errorf("Size() = %d, want %d", got, len(tt.data))
			}
		})
	}
}

// --- 4. ReadAt reads correct data at various offsets ---

func TestReadAt_VariousOffsets(t *testing.T) {
	data := []byte("ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	path := createTempFile(t, data)

	reader, err := NewDiskReader(path)
	if err != nil {
		t.Fatalf("NewDiskReader error: %v", err)
	}
	defer reader.Close()

	tests := []struct {
		name     string
		offset   int64
		readLen  int
		expected string
	}{
		{"start", 0, 5, "ABCDE"},
		{"middle", 10, 5, "KLMNO"},
		{"near end", 23, 3, "XYZ"},
		{"single byte at offset 13", 13, 1, "N"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf := make([]byte, tt.readLen)
			n, err := reader.ReadAt(buf, tt.offset)
			if err != nil {
				t.Fatalf("ReadAt(%d) error: %v", tt.offset, err)
			}
			if n != tt.readLen {
				t.Errorf("ReadAt(%d) read %d bytes, want %d", tt.offset, n, tt.readLen)
			}
			if string(buf) != tt.expected {
				t.Errorf("ReadAt(%d) = %q, want %q", tt.offset, string(buf), tt.expected)
			}
		})
	}
}

// --- 5. ReadAt beyond file end (EOF) ---

func TestReadAt_BeyondEOF(t *testing.T) {
	data := []byte("short")
	path := createTempFile(t, data)

	reader, err := NewDiskReader(path)
	if err != nil {
		t.Fatalf("NewDiskReader error: %v", err)
	}
	defer reader.Close()

	// Read starting beyond the file
	buf := make([]byte, 10)
	_, err = reader.ReadAt(buf, 100)
	if err != io.EOF {
		t.Errorf("expected io.EOF for read beyond file end, got: %v", err)
	}

	// Read that partially overlaps EOF
	buf = make([]byte, 10)
	n, err := reader.ReadAt(buf, 2)
	if err != io.EOF {
		t.Errorf("expected io.EOF for partial read at end, got: %v", err)
	}
	if n != 3 {
		t.Errorf("expected 3 bytes read, got %d", n)
	}
	if string(buf[:n]) != "ort" {
		t.Errorf("expected %q, got %q", "ort", string(buf[:n]))
	}
}

// --- 6. ReadAt at offset 0 ---

func TestReadAt_OffsetZero(t *testing.T) {
	data := []byte("%PDF-1.4 this is a fake pdf header")
	path := createTempFile(t, data)

	reader, err := NewDiskReader(path)
	if err != nil {
		t.Fatalf("NewDiskReader error: %v", err)
	}
	defer reader.Close()

	buf := make([]byte, 8)
	n, err := reader.ReadAt(buf, 0)
	if err != nil {
		t.Fatalf("ReadAt(0) error: %v", err)
	}
	if n != 8 {
		t.Errorf("ReadAt(0) read %d bytes, want 8", n)
	}
	if string(buf) != "%PDF-1.4" {
		t.Errorf("ReadAt(0) = %q, want %q", string(buf), "%PDF-1.4")
	}
}

// --- 7. Close() works ---

func TestClose(t *testing.T) {
	data := []byte("closeable content")
	path := createTempFile(t, data)

	reader, err := NewDiskReader(path)
	if err != nil {
		t.Fatalf("NewDiskReader error: %v", err)
	}

	// Close should not return an error
	if err := reader.Close(); err != nil {
		t.Fatalf("Close() returned error: %v", err)
	}

	// After closing, ReadAt should fail
	buf := make([]byte, 5)
	_, err = reader.ReadAt(buf, 0)
	if err == nil {
		t.Error("expected error after Close(), got nil")
	}
}

// --- 8. ScanBlocks reads entire file in correct block sizes ---

func TestScanBlocks_EntireFile(t *testing.T) {
	data := []byte("AAAAABBBBBCCCCCDDDDDEEEEE") // 25 bytes
	path := createTempFile(t, data)

	reader, err := NewDiskReader(path)
	if err != nil {
		t.Fatalf("NewDiskReader error: %v", err)
	}
	defer reader.Close()

	blockSize := 5
	var offsets []int64
	var blocks []string

	err = reader.ScanBlocks(blockSize, func(offset int64, block []byte) bool {
		offsets = append(offsets, offset)
		blocks = append(blocks, string(block))
		return true
	})
	if err != nil {
		t.Fatalf("ScanBlocks error: %v", err)
	}

	expectedOffsets := []int64{0, 5, 10, 15, 20}
	expectedBlocks := []string{"AAAAA", "BBBBB", "CCCCC", "DDDDD", "EEEEE"}

	if len(offsets) != len(expectedOffsets) {
		t.Fatalf("got %d blocks, want %d", len(offsets), len(expectedOffsets))
	}

	for i := range expectedOffsets {
		if offsets[i] != expectedOffsets[i] {
			t.Errorf("block %d: offset = %d, want %d", i, offsets[i], expectedOffsets[i])
		}
		if blocks[i] != expectedBlocks[i] {
			t.Errorf("block %d: data = %q, want %q", i, blocks[i], expectedBlocks[i])
		}
	}
}

func TestScanBlocks_UnevenBlockSize(t *testing.T) {
	data := []byte("1234567") // 7 bytes with block size 3 => 3+3+1
	path := createTempFile(t, data)

	reader, err := NewDiskReader(path)
	if err != nil {
		t.Fatalf("NewDiskReader error: %v", err)
	}
	defer reader.Close()

	var blocks []string

	err = reader.ScanBlocks(3, func(offset int64, block []byte) bool {
		blocks = append(blocks, string(block))
		return true
	})
	if err != nil {
		t.Fatalf("ScanBlocks error: %v", err)
	}

	expected := []string{"123", "456", "7"}
	if len(blocks) != len(expected) {
		t.Fatalf("got %d blocks, want %d", len(blocks), len(expected))
	}
	for i, exp := range expected {
		if blocks[i] != exp {
			t.Errorf("block %d = %q, want %q", i, blocks[i], exp)
		}
	}
}

// --- 9. ScanBlocks with block size larger than file ---

func TestScanBlocks_BlockLargerThanFile(t *testing.T) {
	data := []byte("tiny")
	path := createTempFile(t, data)

	reader, err := NewDiskReader(path)
	if err != nil {
		t.Fatalf("NewDiskReader error: %v", err)
	}
	defer reader.Close()

	var callCount int
	var receivedData string

	err = reader.ScanBlocks(4096, func(offset int64, block []byte) bool {
		callCount++
		receivedData = string(block)
		return true
	})
	if err != nil {
		t.Fatalf("ScanBlocks error: %v", err)
	}

	if callCount != 1 {
		t.Errorf("expected 1 callback, got %d", callCount)
	}
	if receivedData != "tiny" {
		t.Errorf("expected %q, got %q", "tiny", receivedData)
	}
}

// --- 10. ScanBlocks early termination (callback returns false) ---

func TestScanBlocks_EarlyTermination(t *testing.T) {
	data := make([]byte, 100) // 100 bytes
	for i := range data {
		data[i] = byte(i)
	}
	path := createTempFile(t, data)

	reader, err := NewDiskReader(path)
	if err != nil {
		t.Fatalf("NewDiskReader error: %v", err)
	}
	defer reader.Close()

	var callCount int

	err = reader.ScanBlocks(10, func(offset int64, block []byte) bool {
		callCount++
		// Stop after 3 blocks
		return callCount < 3
	})
	if err != nil {
		t.Fatalf("ScanBlocks error: %v", err)
	}

	if callCount != 3 {
		t.Errorf("expected 3 callbacks before stop, got %d", callCount)
	}
}

// --- 11. ScanBlocks with empty file ---

func TestScanBlocks_EmptyFile(t *testing.T) {
	path := createTempFile(t, []byte{})

	reader, err := NewDiskReader(path)
	if err != nil {
		t.Fatalf("NewDiskReader error: %v", err)
	}
	defer reader.Close()

	var callCount int

	err = reader.ScanBlocks(512, func(offset int64, block []byte) bool {
		callCount++
		return true
	})
	if err != nil {
		t.Fatalf("ScanBlocks error: %v", err)
	}

	if callCount != 0 {
		t.Errorf("expected 0 callbacks for empty file, got %d", callCount)
	}
}

// --- 12. Concurrent ReadAt calls (verify mutex works) ---

func TestReadAt_Concurrent(t *testing.T) {
	// Create a file with known pattern: offset i contains byte i%256
	size := 4096
	data := make([]byte, size)
	for i := range data {
		data[i] = byte(i % 256)
	}
	path := createTempFile(t, data)

	reader, err := NewDiskReader(path)
	if err != nil {
		t.Fatalf("NewDiskReader error: %v", err)
	}
	defer reader.Close()

	const goroutines = 50
	const readsPerGoroutine = 20

	var wg sync.WaitGroup
	errors := make(chan error, goroutines*readsPerGoroutine)

	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for r := 0; r < readsPerGoroutine; r++ {
				offset := int64((id*readsPerGoroutine + r) % (size - 16))
				buf := make([]byte, 16)
				n, err := reader.ReadAt(buf, offset)
				if err != nil {
					errors <- err
					return
				}
				if n != 16 {
					errors <- fmt.Errorf("goroutine %d read %d: expected 16 bytes, got %d", id, r, n)
					return
				}
				// Verify data correctness
				for i := 0; i < 16; i++ {
					expected := byte((int(offset) + i) % 256)
					if buf[i] != expected {
						errors <- fmt.Errorf("goroutine %d read %d: byte %d at offset %d = %d, want %d",
							id, r, i, offset+int64(i), buf[i], expected)
						return
					}
				}
			}
		}(g)
	}

	wg.Wait()
	close(errors)

	for err := range errors {
		t.Errorf("concurrent read error: %v", err)
	}
}
