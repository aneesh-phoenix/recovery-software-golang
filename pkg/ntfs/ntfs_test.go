package ntfs

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/aneesh/recovery-soft/pkg/scanner"
)

// helper to create a temp file with given content and return a DiskReader
func newTestReader(t *testing.T, data []byte) *scanner.DiskReader {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "disk.img")
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("failed to write test image: %v", err)
	}
	reader, err := scanner.NewDiskReader(path)
	if err != nil {
		t.Fatalf("failed to create DiskReader: %v", err)
	}
	t.Cleanup(func() { reader.Close() })
	return reader
}

// buildBootSector creates a minimal valid NTFS boot sector (512 bytes).
func buildBootSector(bytesPerSector uint16, sectorsPerCluster uint8, mftCluster, mftMirrCluster uint64) []byte {
	buf := make([]byte, 512)
	// Jump instruction (3 bytes)
	buf[0] = 0xEB
	buf[1] = 0x52
	buf[2] = 0x90
	// NTFS signature at offset 3
	copy(buf[3:7], "NTFS")
	// BPB: BytesPerSector at 0x0B
	binary.LittleEndian.PutUint16(buf[0x0B:0x0D], bytesPerSector)
	// BPB: SectorsPerCluster at 0x0D
	buf[0x0D] = sectorsPerCluster
	// MFTCluster at 0x30
	binary.LittleEndian.PutUint64(buf[0x30:0x38], mftCluster)
	// MFTMirrCluster at 0x38
	binary.LittleEndian.PutUint64(buf[0x38:0x40], mftMirrCluster)
	return buf
}

// buildMFTEntry creates a minimal valid MFT entry (1024 bytes).
func buildMFTEntry(recordNum uint32, flags uint16, firstAttrOffset uint16) []byte {
	buf := make([]byte, MFTEntrySize)
	// FILE signature
	copy(buf[0:4], "FILE")
	// Fixup offset at 0x04 (point past header, no real fixup)
	binary.LittleEndian.PutUint16(buf[0x04:0x06], 0x30)
	// Fixup count at 0x06 (1 = signature only, no replacements needed in practice)
	binary.LittleEndian.PutUint16(buf[0x06:0x08], 0x01)
	// First attribute offset at 0x14
	binary.LittleEndian.PutUint16(buf[0x14:0x16], firstAttrOffset)
	// Flags at 0x16
	binary.LittleEndian.PutUint16(buf[0x16:0x18], flags)
	// Record number at 0x2C
	binary.LittleEndian.PutUint32(buf[0x2C:0x30], recordNum)
	return buf
}

// =============================================================================
// decodeUTF16 tests
// =============================================================================

func TestDecodeUTF16_ASCII(t *testing.T) {
	// "Hello" in UTF-16LE
	input := []byte{'H', 0, 'e', 0, 'l', 0, 'l', 0, 'o', 0}
	got := decodeUTF16(input)
	if got != "Hello" {
		t.Errorf("decodeUTF16 ASCII: got %q, want %q", got, "Hello")
	}
}

func TestDecodeUTF16_Empty(t *testing.T) {
	got := decodeUTF16([]byte{})
	if got != "" {
		t.Errorf("decodeUTF16 empty: got %q, want %q", got, "")
	}
}

func TestDecodeUTF16_Unicode(t *testing.T) {
	// "café" in UTF-16LE: c=0x0063, a=0x0061, f=0x0066, é=0x00E9
	input := []byte{0x63, 0x00, 0x61, 0x00, 0x66, 0x00, 0xE9, 0x00}
	got := decodeUTF16(input)
	if got != "café" {
		t.Errorf("decodeUTF16 unicode: got %q, want %q", got, "café")
	}
}

func TestDecodeUTF16_OddLength(t *testing.T) {
	// "AB" in UTF-16LE with an extra trailing byte that should be truncated
	input := []byte{'A', 0, 'B', 0, 0xFF}
	got := decodeUTF16(input)
	if got != "AB" {
		t.Errorf("decodeUTF16 odd-length: got %q, want %q", got, "AB")
	}
}

// =============================================================================
// decodeDataRuns tests
// =============================================================================

func TestDecodeDataRuns_SingleRun(t *testing.T) {
	// Header: 0x11 = length 1 byte, offset 1 byte
	// Length: 0x04 (4 clusters)
	// Offset: 0x0A (cluster 10)
	// Terminator: 0x00
	data := []byte{0x11, 0x04, 0x0A, 0x00}
	runs := decodeDataRuns(data)
	if len(runs) != 1 {
		t.Fatalf("expected 1 run, got %d", len(runs))
	}
	if runs[0].ClusterCount != 4 {
		t.Errorf("run[0].ClusterCount = %d, want 4", runs[0].ClusterCount)
	}
	if runs[0].ClusterOffset != 10 {
		t.Errorf("run[0].ClusterOffset = %d, want 10", runs[0].ClusterOffset)
	}
}

func TestDecodeDataRuns_MultipleRuns(t *testing.T) {
	// Run 1: header 0x11, length=0x08, offset=0x14 (cluster 20, absolute)
	// Run 2: header 0x11, length=0x03, offset=0x0A (relative +10, absolute=30)
	// Terminator: 0x00
	data := []byte{
		0x11, 0x08, 0x14, // run1: 8 clusters at offset 20
		0x11, 0x03, 0x0A, // run2: 3 clusters at offset 20+10=30
		0x00,             // terminator
	}
	runs := decodeDataRuns(data)
	if len(runs) != 2 {
		t.Fatalf("expected 2 runs, got %d", len(runs))
	}
	if runs[0].ClusterOffset != 20 {
		t.Errorf("run[0].ClusterOffset = %d, want 20", runs[0].ClusterOffset)
	}
	if runs[0].ClusterCount != 8 {
		t.Errorf("run[0].ClusterCount = %d, want 8", runs[0].ClusterCount)
	}
	if runs[1].ClusterOffset != 30 {
		t.Errorf("run[1].ClusterOffset = %d, want 30", runs[1].ClusterOffset)
	}
	if runs[1].ClusterCount != 3 {
		t.Errorf("run[1].ClusterCount = %d, want 3", runs[1].ClusterCount)
	}
}

func TestDecodeDataRuns_EmptyTerminator(t *testing.T) {
	// Just a terminator byte
	data := []byte{0x00}
	runs := decodeDataRuns(data)
	if len(runs) != 0 {
		t.Errorf("expected 0 runs, got %d", len(runs))
	}
}

func TestDecodeDataRuns_NegativeOffset(t *testing.T) {
	// Run 1: header 0x11, length=0x05, offset=0x20 (cluster 32)
	// Run 2: header 0x11, length=0x02, offset=0xF0 (signed -16, so absolute = 32 - 16 = 16)
	// 0xF0 as a signed int8 = -16
	data := []byte{
		0x11, 0x05, 0x20, // run1: 5 clusters at cluster 32
		0x11, 0x02, 0xF0, // run2: 2 clusters at cluster 32 + (-16) = 16
		0x00,
	}
	runs := decodeDataRuns(data)
	if len(runs) != 2 {
		t.Fatalf("expected 2 runs, got %d", len(runs))
	}
	if runs[0].ClusterOffset != 32 {
		t.Errorf("run[0].ClusterOffset = %d, want 32", runs[0].ClusterOffset)
	}
	if runs[1].ClusterOffset != 16 {
		t.Errorf("run[1].ClusterOffset = %d, want 16", runs[1].ClusterOffset)
	}
}

// =============================================================================
// applyFixup tests
// =============================================================================

func TestApplyFixup_Valid(t *testing.T) {
	// Create a 1024-byte buffer simulating an MFT entry with 2 sectors (512 each)
	buf := make([]byte, MFTEntrySize)
	// Fixup array at offset 0x30:
	// Entry 0 (signature): 0xAA 0xBB
	// Entry 1 (replaces last 2 bytes of sector 1): 0x11 0x22
	// Entry 2 (replaces last 2 bytes of sector 2): 0x33 0x44
	fixupOffset := 0x30
	fixupCount := 3 // signature + 2 sector fixups

	// Write fixup signature
	buf[fixupOffset] = 0xAA
	buf[fixupOffset+1] = 0xBB
	// Fixup for sector 1 (replaces bytes at 510-511)
	buf[fixupOffset+2] = 0x11
	buf[fixupOffset+3] = 0x22
	// Fixup for sector 2 (replaces bytes at 1022-1023)
	buf[fixupOffset+4] = 0x33
	buf[fixupOffset+5] = 0x44

	// Set the sector-end bytes to the fixup signature (as NTFS does)
	buf[510] = 0xAA
	buf[511] = 0xBB
	buf[1022] = 0xAA
	buf[1023] = 0xBB

	err := applyFixup(buf, fixupOffset, fixupCount)
	if err != nil {
		t.Fatalf("applyFixup returned error: %v", err)
	}

	// Verify fixup was applied
	if buf[510] != 0x11 || buf[511] != 0x22 {
		t.Errorf("sector 1 fixup: got [%x %x], want [11 22]", buf[510], buf[511])
	}
	if buf[1022] != 0x33 || buf[1023] != 0x44 {
		t.Errorf("sector 2 fixup: got [%x %x], want [33 44]", buf[1022], buf[1023])
	}
}

func TestApplyFixup_InvalidTooShort(t *testing.T) {
	buf := make([]byte, 10)
	// fixupCount < 2 should return error
	err := applyFixup(buf, 0, 1)
	if err == nil {
		t.Error("expected error for fixupCount < 2, got nil")
	}
}

// =============================================================================
// ParseBootSector tests
// =============================================================================

func TestParseBootSector_Valid(t *testing.T) {
	bootData := buildBootSector(512, 8, 786432, 2)
	// Pad to at least 512 bytes (already done by buildBootSector)
	reader := newTestReader(t, bootData)

	parser, err := NewParser(reader, 0)
	if err != nil {
		t.Fatalf("NewParser failed: %v", err)
	}

	err = parser.ParseBootSector()
	if err != nil {
		t.Fatalf("ParseBootSector failed: %v", err)
	}

	boot := parser.BootInfo()
	if boot == nil {
		t.Fatal("BootInfo returned nil")
	}
	if boot.BytesPerSector != 512 {
		t.Errorf("BytesPerSector = %d, want 512", boot.BytesPerSector)
	}
	if boot.SectorsPerCluster != 8 {
		t.Errorf("SectorsPerCluster = %d, want 8", boot.SectorsPerCluster)
	}
	if boot.ClusterSize != 4096 {
		t.Errorf("ClusterSize = %d, want 4096", boot.ClusterSize)
	}
	if boot.MFTCluster != 786432 {
		t.Errorf("MFTCluster = %d, want 786432", boot.MFTCluster)
	}
	if boot.MFTMirrCluster != 2 {
		t.Errorf("MFTMirrCluster = %d, want 2", boot.MFTMirrCluster)
	}
}

func TestParseBootSector_NonNTFS(t *testing.T) {
	// Create a 512-byte buffer without NTFS signature
	data := make([]byte, 512)
	copy(data[3:7], "EXT4") // Wrong signature
	reader := newTestReader(t, data)

	parser, err := NewParser(reader, 0)
	if err != nil {
		t.Fatalf("NewParser failed: %v", err)
	}

	err = parser.ParseBootSector()
	if err == nil {
		t.Error("expected error for non-NTFS data, got nil")
	}
}

// =============================================================================
// MFTOffset tests
// =============================================================================

func TestMFTOffset_Calculation(t *testing.T) {
	bootData := buildBootSector(512, 8, 100, 2) // cluster 100, clusterSize=4096
	// Image must be large enough: partition at offset 1024, boot sector needs 512 bytes there
	img := make([]byte, 2048)
	copy(img[1024:1024+512], bootData)
	reader := newTestReader(t, img)

	parser, err := NewParser(reader, 1024) // partition starts at offset 1024
	if err != nil {
		t.Fatalf("NewParser failed: %v", err)
	}

	// Before parsing boot sector, MFTOffset should return 0
	if offset := parser.MFTOffset(); offset != 0 {
		t.Errorf("MFTOffset before ParseBootSector = %d, want 0", offset)
	}

	err = parser.ParseBootSector()
	if err != nil {
		t.Fatalf("ParseBootSector failed: %v", err)
	}

	// MFTOffset = partition + MFTCluster * ClusterSize = 1024 + 100 * 4096 = 410624
	expected := int64(1024 + 100*4096)
	if offset := parser.MFTOffset(); offset != expected {
		t.Errorf("MFTOffset = %d, want %d", offset, expected)
	}
}

// =============================================================================
// NewParser tests
// =============================================================================

func TestNewParser_Creation(t *testing.T) {
	data := make([]byte, 512)
	reader := newTestReader(t, data)

	parser, err := NewParser(reader, 0)
	if err != nil {
		t.Fatalf("NewParser failed: %v", err)
	}
	if parser == nil {
		t.Fatal("NewParser returned nil parser")
	}
	// BootInfo should be nil before parsing
	if parser.BootInfo() != nil {
		t.Error("BootInfo should be nil before ParseBootSector")
	}
}

// =============================================================================
// ReadMFTEntry tests
// =============================================================================

func TestReadMFTEntry_ValidRecord(t *testing.T) {
	// Build a valid MFT entry with InUse flag set, record number 5
	firstAttrOffset := uint16(0x38) // attributes start at offset 0x38
	entry := buildMFTEntry(5, 0x01, firstAttrOffset) // InUse=true, not directory

	// Add an end-of-attributes marker at the first attribute offset
	binary.LittleEndian.PutUint32(entry[firstAttrOffset:firstAttrOffset+4], AttrEnd)

	// Build the disk image: boot sector + padding to place MFT entry at a known offset
	bootData := buildBootSector(512, 8, 0, 0)
	img := make([]byte, 2048)
	copy(img[0:512], bootData)
	copy(img[1024:2048], entry)

	reader := newTestReader(t, img)
	parser, err := NewParser(reader, 0)
	if err != nil {
		t.Fatalf("NewParser failed: %v", err)
	}
	err = parser.ParseBootSector()
	if err != nil {
		t.Fatalf("ParseBootSector failed: %v", err)
	}

	mftEntry, err := parser.ReadMFTEntry(1024)
	if err != nil {
		t.Fatalf("ReadMFTEntry failed: %v", err)
	}
	if mftEntry.RecordNumber != 5 {
		t.Errorf("RecordNumber = %d, want 5", mftEntry.RecordNumber)
	}
	if !mftEntry.InUse {
		t.Error("expected InUse=true")
	}
	if mftEntry.IsDirectory {
		t.Error("expected IsDirectory=false")
	}
}

func TestReadMFTEntry_InvalidSignature(t *testing.T) {
	// Create a 1024-byte block without FILE signature
	data := make([]byte, 2048)
	copy(data[0:512], buildBootSector(512, 8, 0, 0))
	copy(data[1024:1028], "BAAD") // Invalid signature

	reader := newTestReader(t, data)
	parser, err := NewParser(reader, 0)
	if err != nil {
		t.Fatalf("NewParser failed: %v", err)
	}
	_ = parser.ParseBootSector()

	_, err = parser.ReadMFTEntry(1024)
	if err == nil {
		t.Error("expected error for invalid MFT signature, got nil")
	}
}

// =============================================================================
// RecoverFileData tests
// =============================================================================

func TestRecoverFileData_ReadsDataRuns(t *testing.T) {
	// Build a disk image:
	// - Boot sector at offset 0 (partition offset = 0)
	// - File data at cluster 2 (offset = 2 * 4096 = 8192)
	// BytesPerSector=512, SectorsPerCluster=8 => ClusterSize=4096
	bootData := buildBootSector(512, 8, 0, 0)
	imgSize := 16384 // enough to hold cluster 2
	img := make([]byte, imgSize)
	copy(img[0:512], bootData)

	// Write known data at cluster 2 (offset 8192)
	fileContent := []byte("RECOVERED FILE DATA CONTENT!")
	copy(img[8192:], fileContent)

	reader := newTestReader(t, img)
	parser, err := NewParser(reader, 0)
	if err != nil {
		t.Fatalf("NewParser failed: %v", err)
	}
	err = parser.ParseBootSector()
	if err != nil {
		t.Fatalf("ParseBootSector failed: %v", err)
	}

	// Create an MFTEntry pointing to cluster 2, 1 cluster, fileSize = len(fileContent)
	mftEntry := &MFTEntry{
		FileName: "test.pdf",
		FileSize: int64(len(fileContent)),
		DataRuns: []DataRun{
			{ClusterOffset: 2, ClusterCount: 1},
		},
	}

	data, err := parser.RecoverFileData(mftEntry)
	if err != nil {
		t.Fatalf("RecoverFileData failed: %v", err)
	}
	if string(data) != string(fileContent) {
		t.Errorf("RecoverFileData got %q, want %q", string(data), string(fileContent))
	}
}

// =============================================================================
// ScanMFT tests
// =============================================================================

func TestScanMFT_IteratesEntries(t *testing.T) {
	// Build disk image with boot sector pointing MFT at cluster 1 (offset 4096)
	// Place 3 MFT entries starting at offset 4096
	bootData := buildBootSector(512, 8, 1, 0) // MFTCluster=1, ClusterSize=4096

	imgSize := 4096 + 3*MFTEntrySize + 4096 // enough room
	img := make([]byte, imgSize)
	copy(img[0:512], bootData)

	// Place 3 valid MFT entries at offset 4096, 5120, 6144
	firstAttrOffset := uint16(0x38)
	for i := 0; i < 3; i++ {
		entry := buildMFTEntry(uint32(i), 0x01, firstAttrOffset)
		binary.LittleEndian.PutUint32(entry[firstAttrOffset:firstAttrOffset+4], AttrEnd)
		offset := 4096 + i*MFTEntrySize
		copy(img[offset:offset+MFTEntrySize], entry)
	}

	reader := newTestReader(t, img)
	parser, err := NewParser(reader, 0)
	if err != nil {
		t.Fatalf("NewParser failed: %v", err)
	}
	err = parser.ParseBootSector()
	if err != nil {
		t.Fatalf("ParseBootSector failed: %v", err)
	}

	var entries []*MFTEntry
	err = parser.ScanMFT(3, func(entry *MFTEntry) {
		entries = append(entries, entry)
	})
	if err != nil {
		t.Fatalf("ScanMFT failed: %v", err)
	}
	if len(entries) != 3 {
		t.Errorf("ScanMFT found %d entries, want 3", len(entries))
	}
	for i, e := range entries {
		if e.RecordNumber != uint32(i) {
			t.Errorf("entry[%d].RecordNumber = %d, want %d", i, e.RecordNumber, i)
		}
	}
}

func TestScanMFT_SkipsInvalidEntries(t *testing.T) {
	// Build disk with MFT at cluster 1; first entry valid, second invalid, third valid
	bootData := buildBootSector(512, 8, 1, 0)
	imgSize := 4096 + 3*MFTEntrySize + 4096
	img := make([]byte, imgSize)
	copy(img[0:512], bootData)

	firstAttrOffset := uint16(0x38)

	// Entry 0: valid
	entry0 := buildMFTEntry(0, 0x01, firstAttrOffset)
	binary.LittleEndian.PutUint32(entry0[firstAttrOffset:firstAttrOffset+4], AttrEnd)
	copy(img[4096:4096+MFTEntrySize], entry0)

	// Entry 1: invalid (bad signature)
	entry1 := make([]byte, MFTEntrySize)
	copy(entry1[0:4], "BAAD")
	copy(img[4096+MFTEntrySize:4096+2*MFTEntrySize], entry1)

	// Entry 2: valid
	entry2 := buildMFTEntry(2, 0x01, firstAttrOffset)
	binary.LittleEndian.PutUint32(entry2[firstAttrOffset:firstAttrOffset+4], AttrEnd)
	copy(img[4096+2*MFTEntrySize:4096+3*MFTEntrySize], entry2)

	reader := newTestReader(t, img)
	parser, err := NewParser(reader, 0)
	if err != nil {
		t.Fatalf("NewParser failed: %v", err)
	}
	err = parser.ParseBootSector()
	if err != nil {
		t.Fatalf("ParseBootSector failed: %v", err)
	}

	var entries []*MFTEntry
	err = parser.ScanMFT(3, func(entry *MFTEntry) {
		entries = append(entries, entry)
	})
	if err != nil {
		t.Fatalf("ScanMFT failed: %v", err)
	}
	// Should have 2 valid entries (skipping the BAAD one)
	if len(entries) != 2 {
		t.Errorf("ScanMFT found %d entries, want 2", len(entries))
	}
}
