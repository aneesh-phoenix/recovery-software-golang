package ext4

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/aneesh/recovery-soft/pkg/scanner"
)

// buildSuperblock creates a raw 1024-byte superblock buffer with the given parameters.
func buildSuperblock(totalInodes, blocksLo, blocksPerGroup, inodesPerGroup uint32, logBlockSize uint32, inodeSize uint16, magic uint16, firstDataBlock uint32, groupDescSize uint16) []byte {
	buf := make([]byte, 1024)
	binary.LittleEndian.PutUint32(buf[0x00:], totalInodes)
	binary.LittleEndian.PutUint32(buf[0x04:], blocksLo)
	binary.LittleEndian.PutUint32(buf[0x14:], firstDataBlock)
	binary.LittleEndian.PutUint32(buf[0x18:], logBlockSize)
	binary.LittleEndian.PutUint32(buf[0x20:], blocksPerGroup)
	binary.LittleEndian.PutUint32(buf[0x28:], inodesPerGroup)
	binary.LittleEndian.PutUint16(buf[0x38:], magic)
	binary.LittleEndian.PutUint16(buf[0x58:], inodeSize)
	binary.LittleEndian.PutUint16(buf[0xFE:], groupDescSize)
	// Feature incompat — no 64-bit flag
	binary.LittleEndian.PutUint32(buf[0x60:], 0x00)
	// Blocks hi
	binary.LittleEndian.PutUint32(buf[0x150:], 0)
	return buf
}

// buildGroupDescriptor creates a raw 32-byte group descriptor.
func buildGroupDescriptor(blockBitmap, inodeBitmap, inodeTable uint32, freeBlocks, freeInodes uint16) []byte {
	buf := make([]byte, 32)
	binary.LittleEndian.PutUint32(buf[0x00:], blockBitmap)
	binary.LittleEndian.PutUint32(buf[0x04:], inodeBitmap)
	binary.LittleEndian.PutUint32(buf[0x08:], inodeTable)
	binary.LittleEndian.PutUint16(buf[0x0C:], freeBlocks)
	binary.LittleEndian.PutUint16(buf[0x0E:], freeInodes)
	return buf
}

// buildInode creates a raw 256-byte inode buffer.
func buildInode(mode uint16, sizeLo uint32, deletionTime uint32, linkCount uint16, blocksLo uint32, flags uint32, blockArea []byte) []byte {
	buf := make([]byte, 256)
	binary.LittleEndian.PutUint16(buf[0x00:], mode)
	binary.LittleEndian.PutUint32(buf[0x04:], sizeLo)
	binary.LittleEndian.PutUint32(buf[0x14:], deletionTime)
	binary.LittleEndian.PutUint16(buf[0x1A:], linkCount)
	binary.LittleEndian.PutUint32(buf[0x1C:], blocksLo)
	binary.LittleEndian.PutUint32(buf[0x20:], flags)
	if len(blockArea) > 60 {
		blockArea = blockArea[:60]
	}
	copy(buf[0x28:], blockArea)
	return buf
}

// buildExtentHeader creates a 12-byte extent header.
func buildExtentHeader(entries, max, depth uint16) []byte {
	buf := make([]byte, 12)
	binary.LittleEndian.PutUint16(buf[0:], 0xF30A) // magic
	binary.LittleEndian.PutUint16(buf[2:], entries)
	binary.LittleEndian.PutUint16(buf[4:], max)
	binary.LittleEndian.PutUint16(buf[6:], depth)
	return buf
}

// buildExtentEntry creates a 12-byte extent leaf entry.
func buildExtentEntry(logicalBlock uint32, length uint16, startHi uint16, startLo uint32) []byte {
	buf := make([]byte, 12)
	binary.LittleEndian.PutUint32(buf[0:], logicalBlock)
	binary.LittleEndian.PutUint16(buf[4:], length)
	binary.LittleEndian.PutUint16(buf[6:], startHi)
	binary.LittleEndian.PutUint32(buf[8:], startLo)
	return buf
}

// createTestImage creates a temp file with given data and returns a DiskReader.
func createTestImage(t *testing.T, data []byte) *scanner.DiskReader {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.img")
	err := os.WriteFile(path, data, 0644)
	if err != nil {
		t.Fatalf("failed to write test image: %v", err)
	}
	reader, err := scanner.NewDiskReader(path)
	if err != nil {
		t.Fatalf("failed to open test image: %v", err)
	}
	t.Cleanup(func() { reader.Close() })
	return reader
}

// --- Inode method tests ---

func TestInode_IsDeleted(t *testing.T) {
	tests := []struct {
		name         string
		deletionTime uint32
		linkCount    uint16
		want         bool
	}{
		{"deleted inode", 1625000000, 0, true},
		{"active inode with links", 0, 1, false},
		{"active inode no deletion time", 0, 0, false},
		{"has deletion time but also links", 1625000000, 1, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inode := &Inode{
				DeletionTime: tt.deletionTime,
				LinkCount:    tt.linkCount,
			}
			if got := inode.IsDeleted(); got != tt.want {
				t.Errorf("IsDeleted() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestInode_IsRegularFile(t *testing.T) {
	tests := []struct {
		name string
		mode uint16
		want bool
	}{
		{"regular file", InodeModeRegFile, true},
		{"regular file with perms", InodeModeRegFile | 0644, true},
		{"directory", InodeModeDir, false},
		{"directory with perms", InodeModeDir | 0755, false},
		{"symlink", 0xA000, false},
		{"block device", 0x6000, false},
		{"zero mode", 0x0000, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inode := &Inode{Mode: tt.mode}
			if got := inode.IsRegularFile(); got != tt.want {
				t.Errorf("IsRegularFile() = %v, want %v", got, tt.want)
			}
		})
	}
}

// --- Extent method tests ---

func TestExtent_PhysicalBlock(t *testing.T) {
	tests := []struct {
		name    string
		startHi uint16
		startLo uint32
		want    int64
	}{
		{"32-bit only", 0, 12345, 12345},
		{"hi bits set", 1, 0, 1 << 32},
		{"combined hi and lo", 2, 500, int64(2)<<32 | 500},
		{"max 16-bit hi", 0xFFFF, 0xFFFFFFFF, int64(0xFFFF)<<32 | int64(0xFFFFFFFF)},
		{"zero", 0, 0, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ext := &Extent{
				StartHi: tt.startHi,
				StartLo: tt.startLo,
			}
			if got := ext.PhysicalBlock(); got != tt.want {
				t.Errorf("PhysicalBlock() = %d, want %d", got, tt.want)
			}
		})
	}
}

// --- Parser tests ---

func TestNewParser(t *testing.T) {
	// Create a minimal image
	img := make([]byte, 4096)
	reader := createTestImage(t, img)

	parser, err := NewParser(reader, 0)
	if err != nil {
		t.Fatalf("NewParser() error = %v", err)
	}
	if parser == nil {
		t.Fatal("NewParser() returned nil parser")
	}
}

func TestParseSuperblock_Valid(t *testing.T) {
	// Build image: 1024 bytes padding + 1024 byte superblock
	// Total at least 2048 bytes
	img := make([]byte, 4096)

	sb := buildSuperblock(
		1000,  // totalInodes
		8000,  // blocksLo
		8192,  // blocksPerGroup
		500,   // inodesPerGroup
		2,     // logBlockSize (blockSize = 1024 << 2 = 4096)
		256,   // inodeSize
		SuperblockMagic, // magic
		0,     // firstDataBlock
		32,    // groupDescSize
	)
	copy(img[SuperblockOffset:], sb)

	reader := createTestImage(t, img)
	parser, err := NewParser(reader, 0)
	if err != nil {
		t.Fatalf("NewParser() error = %v", err)
	}

	err = parser.ParseSuperblock()
	if err != nil {
		t.Fatalf("ParseSuperblock() error = %v", err)
	}

	info := parser.SuperblockInfo()
	if info == nil {
		t.Fatal("SuperblockInfo() returned nil")
	}
	if info.Magic != SuperblockMagic {
		t.Errorf("Magic = 0x%04X, want 0x%04X", info.Magic, SuperblockMagic)
	}
	if info.TotalInodes != 1000 {
		t.Errorf("TotalInodes = %d, want 1000", info.TotalInodes)
	}
	if info.BlockSize != 4096 {
		t.Errorf("BlockSize = %d, want 4096", info.BlockSize)
	}
	if info.BlocksPerGroup != 8192 {
		t.Errorf("BlocksPerGroup = %d, want 8192", info.BlocksPerGroup)
	}
	if info.InodesPerGroup != 500 {
		t.Errorf("InodesPerGroup = %d, want 500", info.InodesPerGroup)
	}
	if info.InodeSize != 256 {
		t.Errorf("InodeSize = %d, want 256", info.InodeSize)
	}
	if info.FirstDataBlock != 0 {
		t.Errorf("FirstDataBlock = %d, want 0", info.FirstDataBlock)
	}
	if info.GroupDescSize != 32 {
		t.Errorf("GroupDescSize = %d, want 32", info.GroupDescSize)
	}
	if info.TotalBlocks != 8000 {
		t.Errorf("TotalBlocks = %d, want 8000", info.TotalBlocks)
	}
}

func TestParseSuperblock_WrongMagic(t *testing.T) {
	img := make([]byte, 4096)

	sb := buildSuperblock(100, 1000, 8192, 100, 0, 128, 0xBEEF, 1, 32)
	copy(img[SuperblockOffset:], sb)

	reader := createTestImage(t, img)
	parser, err := NewParser(reader, 0)
	if err != nil {
		t.Fatalf("NewParser() error = %v", err)
	}

	err = parser.ParseSuperblock()
	if err == nil {
		t.Fatal("ParseSuperblock() expected error for wrong magic, got nil")
	}
}

func TestSuperblockInfo_BeforeParse(t *testing.T) {
	img := make([]byte, 4096)
	reader := createTestImage(t, img)

	parser, err := NewParser(reader, 0)
	if err != nil {
		t.Fatalf("NewParser() error = %v", err)
	}

	info := parser.SuperblockInfo()
	if info != nil {
		t.Errorf("SuperblockInfo() before ParseSuperblock should be nil, got %+v", info)
	}
}

func TestReadGroupDescriptor(t *testing.T) {
	// Layout: block size 1024, firstDataBlock=1
	// Superblock at offset 1024 (block 1)
	// GDT starts at block 2 (offset 2048)
	blockSize := uint32(1024)
	logBlockSize := uint32(0) // 1024 << 0 = 1024

	// Image needs at least: superblock area + GDT
	img := make([]byte, 8192)

	sb := buildSuperblock(100, 1000, 8192, 100, logBlockSize, 256, SuperblockMagic, 1, 32)
	copy(img[SuperblockOffset:], sb)

	// Place group descriptor at block 2 (offset 2048), since firstDataBlock=1, GDT at block 2
	gd := buildGroupDescriptor(10, 11, 12, 500, 50)
	gdtOffset := int(2) * int(blockSize) // block (firstDataBlock+1) = block 2
	copy(img[gdtOffset:], gd)

	reader := createTestImage(t, img)
	parser, err := NewParser(reader, 0)
	if err != nil {
		t.Fatalf("NewParser() error = %v", err)
	}
	if err := parser.ParseSuperblock(); err != nil {
		t.Fatalf("ParseSuperblock() error = %v", err)
	}

	desc, err := parser.ReadGroupDescriptor(0)
	if err != nil {
		t.Fatalf("ReadGroupDescriptor(0) error = %v", err)
	}
	if desc.BlockBitmapLo != 10 {
		t.Errorf("BlockBitmapLo = %d, want 10", desc.BlockBitmapLo)
	}
	if desc.InodeBitmapLo != 11 {
		t.Errorf("InodeBitmapLo = %d, want 11", desc.InodeBitmapLo)
	}
	if desc.InodeTableLo != 12 {
		t.Errorf("InodeTableLo = %d, want 12", desc.InodeTableLo)
	}
	if desc.FreeBlocksCount != 500 {
		t.Errorf("FreeBlocksCount = %d, want 500", desc.FreeBlocksCount)
	}
	if desc.FreeInodesCount != 50 {
		t.Errorf("FreeInodesCount = %d, want 50", desc.FreeInodesCount)
	}
}

func TestReadInode(t *testing.T) {
	// Layout: block size 1024, firstDataBlock=1
	// Superblock at offset 1024
	// GDT at block 2 (offset 2048)
	// Inode table at block 5 (offset 5120)
	blockSize := uint32(1024)
	logBlockSize := uint32(0)
	inodeTableBlock := uint32(5)

	// Need enough space: inode table at block 5, inode 1 at offset 5*1024
	img := make([]byte, 16384)

	sb := buildSuperblock(100, 1000, 8192, 100, logBlockSize, 256, SuperblockMagic, 1, 32)
	copy(img[SuperblockOffset:], sb)

	// GDT at block 2 with inode table at block 5
	gd := buildGroupDescriptor(3, 4, inodeTableBlock, 900, 90)
	gdtOffset := 2 * int(blockSize)
	copy(img[gdtOffset:], gd)

	// Build an extent-based inode for inode #1 (index 0 in table)
	extHeader := buildExtentHeader(1, 4, 0)
	extEntry := buildExtentEntry(0, 2, 0, 8) // 2 blocks starting at physical block 8
	blockArea := make([]byte, 60)
	copy(blockArea[0:], extHeader)
	copy(blockArea[12:], extEntry)

	inodeData := buildInode(
		InodeModeRegFile|0644, // mode: regular file
		2048,                  // size
		0,                     // deletionTime (active)
		1,                     // linkCount
		4,                     // blocksLo
		InodeFlagExtents,      // flags: extent-based
		blockArea,
	)
	inodeOffset := int(inodeTableBlock) * int(blockSize) // inode #1 is at index 0
	copy(img[inodeOffset:], inodeData)

	reader := createTestImage(t, img)
	parser, err := NewParser(reader, 0)
	if err != nil {
		t.Fatalf("NewParser() error = %v", err)
	}
	if err := parser.ParseSuperblock(); err != nil {
		t.Fatalf("ParseSuperblock() error = %v", err)
	}

	inode, err := parser.ReadInode(1)
	if err != nil {
		t.Fatalf("ReadInode(1) error = %v", err)
	}
	if inode.Number != 1 {
		t.Errorf("Number = %d, want 1", inode.Number)
	}
	if inode.Mode != InodeModeRegFile|0644 {
		t.Errorf("Mode = 0x%04X, want 0x%04X", inode.Mode, InodeModeRegFile|0644)
	}
	if inode.Size != 2048 {
		t.Errorf("Size = %d, want 2048", inode.Size)
	}
	if inode.LinkCount != 1 {
		t.Errorf("LinkCount = %d, want 1", inode.LinkCount)
	}
	if !inode.IsRegularFile() {
		t.Error("expected IsRegularFile() == true")
	}
	if inode.IsDeleted() {
		t.Error("expected IsDeleted() == false for active inode")
	}
	if inode.Flags&InodeFlagExtents == 0 {
		t.Error("expected extent flag to be set")
	}
	if len(inode.Extents) != 1 {
		t.Fatalf("expected 1 extent, got %d", len(inode.Extents))
	}
	if inode.Extents[0].StartLo != 8 {
		t.Errorf("extent StartLo = %d, want 8", inode.Extents[0].StartLo)
	}
	if inode.Extents[0].Length != 2 {
		t.Errorf("extent Length = %d, want 2", inode.Extents[0].Length)
	}
}

func TestScanDeletedInodes(t *testing.T) {
	// Layout: block size 1024, firstDataBlock=1
	// Superblock at offset 1024
	// GDT at block 2
	// Inode table at block 5
	// We'll put 3 inodes: #1 active, #2 deleted regular file, #3 deleted directory
	blockSize := uint32(1024)
	logBlockSize := uint32(0)
	inodeTableBlock := uint32(5)
	inodesPerGroup := uint32(10)

	img := make([]byte, 32768)

	sb := buildSuperblock(inodesPerGroup, 30, 8192, inodesPerGroup, logBlockSize, 256, SuperblockMagic, 1, 32)
	copy(img[SuperblockOffset:], sb)

	// GDT
	gd := buildGroupDescriptor(3, 4, inodeTableBlock, 900, 90)
	gdtOffset := 2 * int(blockSize)
	copy(img[gdtOffset:], gd)

	inodeBase := int(inodeTableBlock) * int(blockSize)

	// Inode #1: active regular file (should NOT be found)
	extArea1 := make([]byte, 60)
	copy(extArea1, buildExtentHeader(0, 4, 0))
	inode1 := buildInode(InodeModeRegFile|0644, 1024, 0, 1, 2, InodeFlagExtents, extArea1)
	copy(img[inodeBase+0*256:], inode1)

	// Inode #2: deleted regular file (SHOULD be found)
	extArea2 := make([]byte, 60)
	copy(extArea2, buildExtentHeader(1, 4, 0))
	copy(extArea2[12:], buildExtentEntry(0, 1, 0, 10))
	inode2 := buildInode(InodeModeRegFile|0644, 512, 1625000000, 0, 1, InodeFlagExtents, extArea2)
	copy(img[inodeBase+1*256:], inode2)

	// Inode #3: deleted directory (should NOT be found — not regular file)
	extArea3 := make([]byte, 60)
	copy(extArea3, buildExtentHeader(0, 4, 0))
	inode3 := buildInode(InodeModeDir|0755, 4096, 1625000000, 0, 1, InodeFlagExtents, extArea3)
	copy(img[inodeBase+2*256:], inode3)

	reader := createTestImage(t, img)
	parser, err := NewParser(reader, 0)
	if err != nil {
		t.Fatalf("NewParser() error = %v", err)
	}
	if err := parser.ParseSuperblock(); err != nil {
		t.Fatalf("ParseSuperblock() error = %v", err)
	}

	var found []*Inode
	err = parser.ScanDeletedInodes(1, func(inode *Inode) {
		found = append(found, inode)
	})
	if err != nil {
		t.Fatalf("ScanDeletedInodes() error = %v", err)
	}

	if len(found) != 1 {
		t.Fatalf("expected 1 deleted inode, found %d", len(found))
	}
	if found[0].Number != 2 {
		t.Errorf("expected deleted inode #2, got #%d", found[0].Number)
	}
	if !found[0].IsDeleted() {
		t.Error("found inode should be deleted")
	}
	if !found[0].IsRegularFile() {
		t.Error("found inode should be a regular file")
	}
}

func TestRecoverInodeData_Extents(t *testing.T) {
	// Layout: block size 1024, firstDataBlock=1
	// Superblock at offset 1024
	// GDT at block 2
	// Inode table at block 5
	// Data at block 8 (filled with known pattern)
	blockSize := uint32(1024)
	logBlockSize := uint32(0)
	inodeTableBlock := uint32(5)
	dataBlock := uint32(8)

	img := make([]byte, 16384)

	sb := buildSuperblock(100, 1000, 8192, 100, logBlockSize, 256, SuperblockMagic, 1, 32)
	copy(img[SuperblockOffset:], sb)

	// GDT
	gd := buildGroupDescriptor(3, 4, inodeTableBlock, 900, 90)
	gdtOffset := 2 * int(blockSize)
	copy(img[gdtOffset:], gd)

	// Write recognizable data at block 8
	dataOffset := int(dataBlock) * int(blockSize)
	for i := 0; i < int(blockSize); i++ {
		img[dataOffset+i] = byte(i % 256)
	}

	// Inode #1: deleted file with extent pointing to block 8, size 512
	extArea := make([]byte, 60)
	copy(extArea, buildExtentHeader(1, 4, 0))
	copy(extArea[12:], buildExtentEntry(0, 1, 0, dataBlock))
	inodeData := buildInode(InodeModeRegFile|0644, 512, 1625000000, 0, 2, InodeFlagExtents, extArea)
	inodeOffset := int(inodeTableBlock) * int(blockSize)
	copy(img[inodeOffset:], inodeData)

	reader := createTestImage(t, img)
	parser, err := NewParser(reader, 0)
	if err != nil {
		t.Fatalf("NewParser() error = %v", err)
	}
	if err := parser.ParseSuperblock(); err != nil {
		t.Fatalf("ParseSuperblock() error = %v", err)
	}

	inode, err := parser.ReadInode(1)
	if err != nil {
		t.Fatalf("ReadInode(1) error = %v", err)
	}

	data, err := parser.RecoverInodeData(inode)
	if err != nil {
		t.Fatalf("RecoverInodeData() error = %v", err)
	}

	// Size should be trimmed to 512
	if len(data) != 512 {
		t.Fatalf("recovered data length = %d, want 512", len(data))
	}

	// Verify content matches what we wrote
	for i := 0; i < 512; i++ {
		if data[i] != byte(i%256) {
			t.Errorf("data[%d] = %d, want %d", i, data[i], byte(i%256))
			break
		}
	}
}

func TestRecoverInodeData_LegacyBlockPtrs(t *testing.T) {
	// Layout: block size 1024, firstDataBlock=1
	// Superblock at offset 1024
	// GDT at block 2
	// Inode table at block 5
	// Data at blocks 9 and 10
	blockSize := uint32(1024)
	logBlockSize := uint32(0)
	inodeTableBlock := uint32(5)

	img := make([]byte, 16384)

	sb := buildSuperblock(100, 1000, 8192, 100, logBlockSize, 256, SuperblockMagic, 1, 32)
	copy(img[SuperblockOffset:], sb)

	// GDT
	gd := buildGroupDescriptor(3, 4, inodeTableBlock, 900, 90)
	gdtOffset := 2 * int(blockSize)
	copy(img[gdtOffset:], gd)

	// Write data at block 9: all 0xAA
	dataOffset9 := 9 * int(blockSize)
	for i := 0; i < int(blockSize); i++ {
		img[dataOffset9+i] = 0xAA
	}
	// Write data at block 10: all 0xBB
	dataOffset10 := 10 * int(blockSize)
	for i := 0; i < int(blockSize); i++ {
		img[dataOffset10+i] = 0xBB
	}

	// Build block pointer area (no extent flag): direct block ptrs [0]=9, [1]=10
	blockArea := make([]byte, 60)
	binary.LittleEndian.PutUint32(blockArea[0:], 9)  // BlockPtrs[0]
	binary.LittleEndian.PutUint32(blockArea[4:], 10) // BlockPtrs[1]

	// Inode #1: legacy block pointer file, size 1500 (spans 2 blocks)
	inodeData := buildInode(InodeModeRegFile|0644, 1500, 1625000000, 0, 4, 0, blockArea)
	inodeOffset := int(inodeTableBlock) * int(blockSize)
	copy(img[inodeOffset:], inodeData)

	reader := createTestImage(t, img)
	parser, err := NewParser(reader, 0)
	if err != nil {
		t.Fatalf("NewParser() error = %v", err)
	}
	if err := parser.ParseSuperblock(); err != nil {
		t.Fatalf("ParseSuperblock() error = %v", err)
	}

	inode, err := parser.ReadInode(1)
	if err != nil {
		t.Fatalf("ReadInode(1) error = %v", err)
	}

	if inode.Flags&InodeFlagExtents != 0 {
		t.Fatal("expected legacy block pointer inode (no extent flag)")
	}

	data, err := parser.RecoverInodeData(inode)
	if err != nil {
		t.Fatalf("RecoverInodeData() error = %v", err)
	}

	// Size should be trimmed to 1500
	if len(data) != 1500 {
		t.Fatalf("recovered data length = %d, want 1500", len(data))
	}

	// First 1024 bytes should be 0xAA (from block 9)
	for i := 0; i < 1024; i++ {
		if data[i] != 0xAA {
			t.Errorf("data[%d] = 0x%02X, want 0xAA", i, data[i])
			break
		}
	}
	// Bytes 1024-1499 should be 0xBB (from block 10)
	for i := 1024; i < 1500; i++ {
		if data[i] != 0xBB {
			t.Errorf("data[%d] = 0x%02X, want 0xBB", i, data[i])
			break
		}
	}
}

func TestParseSuperblock_WithPartitionOffset(t *testing.T) {
	// Place partition at offset 4096 within the image
	partitionOffset := int64(4096)
	img := make([]byte, 8192)

	sb := buildSuperblock(200, 5000, 8192, 200, 1, 256, SuperblockMagic, 0, 64)
	// Superblock goes at partitionOffset + 1024
	copy(img[partitionOffset+SuperblockOffset:], sb)

	reader := createTestImage(t, img)
	parser, err := NewParser(reader, partitionOffset)
	if err != nil {
		t.Fatalf("NewParser() error = %v", err)
	}

	err = parser.ParseSuperblock()
	if err != nil {
		t.Fatalf("ParseSuperblock() error = %v", err)
	}

	info := parser.SuperblockInfo()
	if info.TotalInodes != 200 {
		t.Errorf("TotalInodes = %d, want 200", info.TotalInodes)
	}
	if info.BlockSize != 2048 { // 1024 << 1
		t.Errorf("BlockSize = %d, want 2048", info.BlockSize)
	}
	if info.GroupDescSize != 64 {
		t.Errorf("GroupDescSize = %d, want 64", info.GroupDescSize)
	}
}
