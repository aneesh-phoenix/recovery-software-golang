// Package ext4 provides ext4 filesystem awareness for file recovery.
// It parses superblock, group descriptors, and inode tables to locate
// deleted files and their data block locations.
package ext4

import (
	"encoding/binary"
	"fmt"

	"github.com/aneesh/recovery-soft/pkg/scanner"
)

// ext4 constants
const (
	SuperblockOffset = 1024
	SuperblockMagic  = 0xEF53
	InodeSize        = 256 // Default for ext4

	// Inode flags
	InodeFlagExtents = 0x80000

	// File type in inode mode
	InodeModeRegFile = 0x8000
	InodeModeDir     = 0x4000
)

// Superblock holds key ext4 superblock fields.
type Superblock struct {
	TotalInodes    uint32
	TotalBlocks    uint64
	BlockSize      uint32
	BlocksPerGroup uint32
	InodesPerGroup uint32
	InodeSize      uint16
	Magic          uint16
	FirstDataBlock uint32
	GroupDescSize  uint16
	Feature64bit   bool
}

// GroupDescriptor represents a block group descriptor.
type GroupDescriptor struct {
	BlockBitmapLo   uint32
	InodeBitmapLo   uint32
	InodeTableLo    uint32
	FreeBlocksCount uint16
	FreeInodesCount uint16
	BlockBitmapHi   uint32
	InodeBitmapHi   uint32
	InodeTableHi    uint32
}

// Inode represents a parsed ext4 inode.
type Inode struct {
	Number       uint32
	Mode         uint16
	Size         int64
	Blocks       uint64
	Flags        uint32
	LinkCount    uint16
	DeletionTime uint32
	Extents      []Extent
	ExtentRoot   []byte
	BlockPtrs    [15]uint32
}

// Extent represents an ext4 extent (for extent-based files).
type Extent struct {
	Block   uint32 // Logical block number
	Length  uint16 // Number of blocks
	StartHi uint16 // High 16 bits of physical block
	StartLo uint32 // Low 32 bits of physical block
}

// IsDeleted returns true if the inode appears to be deleted.
func (i *Inode) IsDeleted() bool {
	return i.DeletionTime != 0 && i.LinkCount == 0
}

// IsRegularFile returns true if the inode is a regular file.
func (i *Inode) IsRegularFile() bool {
	return i.Mode&0xF000 == InodeModeRegFile
}

// PhysicalBlock returns the starting physical block number of an extent.
func (e *Extent) PhysicalBlock() int64 {
	return int64(e.StartHi)<<32 | int64(e.StartLo)
}

// Ext4Parser provides ext4 filesystem parsing capabilities.
type Ext4Parser struct {
	reader    *scanner.DiskReader
	partition int64 // Partition start offset
	sb        *Superblock
}

// NewParser creates a new ext4 parser.
func NewParser(reader *scanner.DiskReader, partitionOffset int64) (*Ext4Parser, error) {
	return &Ext4Parser{
		reader:    reader,
		partition: partitionOffset,
	}, nil
}

// ParseSuperblock reads and validates the ext4 superblock.
func (p *Ext4Parser) ParseSuperblock() error {
	buf := make([]byte, 1024)
	_, err := p.reader.ReadAt(buf, p.partition+SuperblockOffset)
	if err != nil {
		return fmt.Errorf("failed to read superblock: %w", err)
	}

	magic := binary.LittleEndian.Uint16(buf[0x38:0x3A])
	if magic != SuperblockMagic {
		return fmt.Errorf("not an ext4 filesystem (magic: 0x%04X, expected 0x%04X)", magic, SuperblockMagic)
	}

	logBlockSize := binary.LittleEndian.Uint32(buf[0x18:0x1C])

	p.sb = &Superblock{
		TotalInodes:    binary.LittleEndian.Uint32(buf[0x00:0x04]),
		BlockSize:      1024 << logBlockSize,
		BlocksPerGroup: binary.LittleEndian.Uint32(buf[0x20:0x24]),
		InodesPerGroup: binary.LittleEndian.Uint32(buf[0x28:0x2C]),
		InodeSize:      binary.LittleEndian.Uint16(buf[0x58:0x5A]),
		Magic:          magic,
		FirstDataBlock: binary.LittleEndian.Uint32(buf[0x14:0x18]),
		GroupDescSize:  binary.LittleEndian.Uint16(buf[0xFE:0x100]),
	}

	// Total blocks (handle 64-bit)
	blocksLo := binary.LittleEndian.Uint32(buf[0x04:0x08])
	blocksHi := binary.LittleEndian.Uint32(buf[0x150:0x154])
	p.sb.TotalBlocks = uint64(blocksHi)<<32 | uint64(blocksLo)

	// Check for 64-bit feature
	featureIncompat := binary.LittleEndian.Uint32(buf[0x60:0x64])
	p.sb.Feature64bit = featureIncompat&0x80 != 0

	if p.sb.InodeSize == 0 {
		p.sb.InodeSize = 256
	}
	if p.sb.GroupDescSize == 0 {
		p.sb.GroupDescSize = 32
	}

	return nil
}

// SuperblockInfo returns the parsed superblock.
func (p *Ext4Parser) SuperblockInfo() *Superblock {
	return p.sb
}

// ReadGroupDescriptor reads a block group descriptor.
func (p *Ext4Parser) ReadGroupDescriptor(groupNum int) (*GroupDescriptor, error) {
	if p.sb == nil {
		return nil, fmt.Errorf("superblock not parsed")
	}

	// Group descriptors start at the block after the superblock
	gdtBlock := int64(p.sb.FirstDataBlock) + 1
	gdtOffset := p.partition + gdtBlock*int64(p.sb.BlockSize)
	descSize := int64(p.sb.GroupDescSize)
	if descSize < 32 {
		descSize = 32
	}

	offset := gdtOffset + int64(groupNum)*descSize
	buf := make([]byte, descSize)
	_, err := p.reader.ReadAt(buf, offset)
	if err != nil {
		return nil, fmt.Errorf("failed to read group descriptor %d: %w", groupNum, err)
	}

	gd := &GroupDescriptor{
		BlockBitmapLo:   binary.LittleEndian.Uint32(buf[0x00:0x04]),
		InodeBitmapLo:   binary.LittleEndian.Uint32(buf[0x04:0x08]),
		InodeTableLo:    binary.LittleEndian.Uint32(buf[0x08:0x0C]),
		FreeBlocksCount: binary.LittleEndian.Uint16(buf[0x0C:0x0E]),
		FreeInodesCount: binary.LittleEndian.Uint16(buf[0x0E:0x10]),
	}

	// Read high bits if 64-bit feature is enabled
	if p.sb.Feature64bit && descSize >= 64 {
		gd.BlockBitmapHi = binary.LittleEndian.Uint32(buf[0x20:0x24])
		gd.InodeBitmapHi = binary.LittleEndian.Uint32(buf[0x24:0x28])
		gd.InodeTableHi = binary.LittleEndian.Uint32(buf[0x28:0x2C])
	}

	return gd, nil
}

// ReadInode reads and parses an inode by number.
func (p *Ext4Parser) ReadInode(inodeNum uint32) (*Inode, error) {
	if p.sb == nil {
		return nil, fmt.Errorf("superblock not parsed")
	}
	if inodeNum == 0 {
		return nil, fmt.Errorf("invalid inode number 0")
	}

	// Determine which block group the inode belongs to
	groupNum := (inodeNum - 1) / p.sb.InodesPerGroup
	localIndex := (inodeNum - 1) % p.sb.InodesPerGroup

	gd, err := p.ReadGroupDescriptor(int(groupNum))
	if err != nil {
		return nil, err
	}

	// Calculate inode offset
	inodeTableBlock := int64(gd.InodeTableHi)<<32 | int64(gd.InodeTableLo)
	inodeOffset := p.partition + inodeTableBlock*int64(p.sb.BlockSize) + int64(localIndex)*int64(p.sb.InodeSize)

	buf := make([]byte, p.sb.InodeSize)
	_, err = p.reader.ReadAt(buf, inodeOffset)
	if err != nil {
		return nil, fmt.Errorf("failed to read inode %d: %w", inodeNum, err)
	}

	inode := &Inode{
		Number:       inodeNum,
		Mode:         binary.LittleEndian.Uint16(buf[0x00:0x02]),
		LinkCount:    binary.LittleEndian.Uint16(buf[0x1A:0x1C]),
		Flags:        binary.LittleEndian.Uint32(buf[0x20:0x24]),
		DeletionTime: binary.LittleEndian.Uint32(buf[0x14:0x18]),
	}

	// Size (combine low and high for large files)
	sizeLo := binary.LittleEndian.Uint32(buf[0x04:0x08])
	sizeHi := binary.LittleEndian.Uint32(buf[0x6C:0x70])
	inode.Size = int64(sizeHi)<<32 | int64(sizeLo)

	// Block count
	blocksLo := binary.LittleEndian.Uint32(buf[0x1C:0x20])
	inode.Blocks = uint64(blocksLo)

	// Parse block pointers or extents
	if inode.Flags&InodeFlagExtents != 0 {
		inode.ExtentRoot = append([]byte(nil), buf[0x28:0x64]...)
		inode.Extents = parseExtents(inode.ExtentRoot)
	} else {
		for i := 0; i < 15; i++ {
			inode.BlockPtrs[i] = binary.LittleEndian.Uint32(buf[0x28+i*4 : 0x28+i*4+4])
		}
	}

	return inode, nil
}

// ScanDeletedInodes scans for deleted inodes that might contain recoverable data.
func (p *Ext4Parser) ScanDeletedInodes(maxGroups int, callback func(inode *Inode)) error {
	if p.sb == nil {
		return fmt.Errorf("superblock not parsed")
	}

	numGroups := int((p.sb.TotalBlocks + uint64(p.sb.BlocksPerGroup) - 1) / uint64(p.sb.BlocksPerGroup))
	if maxGroups > 0 && numGroups > maxGroups {
		numGroups = maxGroups
	}

	for g := 0; g < numGroups; g++ {
		for i := uint32(0); i < p.sb.InodesPerGroup; i++ {
			inodeNum := uint32(g)*p.sb.InodesPerGroup + i + 1
			inode, err := p.ReadInode(inodeNum)
			if err != nil {
				continue
			}

			if inode.IsDeleted() && inode.IsRegularFile() && inode.Size > 0 {
				callback(inode)
			}
		}
	}

	return nil
}

// RecoverInodeData reads the data blocks for an inode.
func (p *Ext4Parser) RecoverInodeData(inode *Inode) ([]byte, error) {
	if p.sb == nil {
		return nil, fmt.Errorf("superblock not parsed")
	}

	var data []byte

	if inode.Flags&InodeFlagExtents != 0 {
		extents, err := p.collectExtents(inode)
		if err != nil {
			return nil, err
		}
		for _, ext := range extents {
			physBlock := ext.PhysicalBlock()
			if physBlock == 0 {
				continue
			}
			offset := p.partition + physBlock*int64(p.sb.BlockSize)
			size := int64(ext.Length) * int64(p.sb.BlockSize)
			buf := make([]byte, size)
			n, err := p.reader.ReadAt(buf, offset)
			if err != nil && n == 0 {
				continue
			}
			data = append(data, buf[:n]...)
		}
	} else {
		blocks, err := p.collectLegacyBlocks(inode)
		if err != nil {
			return nil, err
		}
		for _, ptr := range blocks {
			if ptr == 0 {
				break
			}
			offset := p.partition + int64(ptr)*int64(p.sb.BlockSize)
			buf := make([]byte, p.sb.BlockSize)
			n, err := p.reader.ReadAt(buf, offset)
			if err != nil && n == 0 {
				continue
			}
			data = append(data, buf[:n]...)
		}
	}

	// Trim to actual file size
	if inode.Size > 0 && int64(len(data)) > inode.Size {
		data = data[:inode.Size]
	}

	return data, nil
}

func (p *Ext4Parser) collectExtents(inode *Inode) ([]Extent, error) {
	if len(inode.ExtentRoot) == 0 {
		return inode.Extents, nil
	}
	return p.parseExtentNode(inode.ExtentRoot, 0)
}

func (p *Ext4Parser) parseExtentNode(data []byte, depth uint16) ([]Extent, error) {
	if len(data) < 12 {
		return nil, fmt.Errorf("extent node is too small")
	}
	magic := binary.LittleEndian.Uint16(data[0:2])
	if magic != 0xF30A {
		return nil, fmt.Errorf("invalid extent magic: 0x%04X", magic)
	}

	entries := binary.LittleEndian.Uint16(data[2:4])
	nodeDepth := binary.LittleEndian.Uint16(data[6:8])
	if nodeDepth == 0 {
		return parseExtentLeafEntries(data, entries), nil
	}
	if depth > 5 {
		return nil, fmt.Errorf("extent tree is too deep")
	}

	var extents []Extent
	for i := uint16(0); i < entries && int(12+i*12+12) <= len(data); i++ {
		off := 12 + i*12
		leafLo := binary.LittleEndian.Uint32(data[off+4 : off+8])
		leafHi := binary.LittleEndian.Uint16(data[off+8 : off+10])
		childBlock := int64(leafHi)<<32 | int64(leafLo)
		if childBlock == 0 {
			continue
		}

		child := make([]byte, p.sb.BlockSize)
		n, err := p.reader.ReadAt(child, p.partition+childBlock*int64(p.sb.BlockSize))
		if err != nil && n == 0 {
			return nil, fmt.Errorf("failed to read extent tree block %d: %w", childBlock, err)
		}
		childExtents, err := p.parseExtentNode(child[:n], depth+1)
		if err != nil {
			return nil, err
		}
		extents = append(extents, childExtents...)
	}
	return extents, nil
}

func (p *Ext4Parser) collectLegacyBlocks(inode *Inode) ([]uint32, error) {
	needed := blocksForSize(inode.Size, int64(p.sb.BlockSize))
	var blocks []uint32
	appendBlock := func(block uint32) bool {
		if block == 0 {
			return true
		}
		blocks = append(blocks, block)
		return int64(len(blocks)) < needed
	}

	for _, ptr := range inode.BlockPtrs[:12] {
		if !appendBlock(ptr) {
			return blocks, nil
		}
	}
	if inode.BlockPtrs[12] != 0 {
		more, err := p.readIndirectBlocks(inode.BlockPtrs[12], 1, needed-int64(len(blocks)))
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, more...)
	}
	if int64(len(blocks)) >= needed {
		return blocks[:needed], nil
	}
	if inode.BlockPtrs[13] != 0 {
		more, err := p.readIndirectBlocks(inode.BlockPtrs[13], 2, needed-int64(len(blocks)))
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, more...)
	}
	if int64(len(blocks)) >= needed {
		return blocks[:needed], nil
	}
	if inode.BlockPtrs[14] != 0 {
		more, err := p.readIndirectBlocks(inode.BlockPtrs[14], 3, needed-int64(len(blocks)))
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, more...)
	}
	if int64(len(blocks)) > needed {
		blocks = blocks[:needed]
	}
	return blocks, nil
}

func (p *Ext4Parser) readIndirectBlocks(block uint32, depth int, limit int64) ([]uint32, error) {
	if block == 0 || limit <= 0 {
		return nil, nil
	}
	buf := make([]byte, p.sb.BlockSize)
	n, err := p.reader.ReadAt(buf, p.partition+int64(block)*int64(p.sb.BlockSize))
	if err != nil && n == 0 {
		return nil, fmt.Errorf("failed to read indirect block %d: %w", block, err)
	}

	var blocks []uint32
	for i := 0; i+4 <= n && int64(len(blocks)) < limit; i += 4 {
		ptr := binary.LittleEndian.Uint32(buf[i : i+4])
		if ptr == 0 {
			continue
		}
		if depth == 1 {
			blocks = append(blocks, ptr)
			continue
		}
		child, err := p.readIndirectBlocks(ptr, depth-1, limit-int64(len(blocks)))
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, child...)
	}
	return blocks, nil
}

func blocksForSize(size, blockSize int64) int64 {
	if size <= 0 || blockSize <= 0 {
		return 0
	}
	return (size + blockSize - 1) / blockSize
}

// parseExtents parses the extent tree from the inode block area (60 bytes).
func parseExtents(data []byte) []Extent {
	if len(data) < 12 {
		return nil
	}

	// Extent header
	magic := binary.LittleEndian.Uint16(data[0:2])
	if magic != 0xF30A {
		return nil
	}

	entries := binary.LittleEndian.Uint16(data[2:4])
	depth := binary.LittleEndian.Uint16(data[6:8])
	if depth != 0 {
		return nil
	}

	return parseExtentLeafEntries(data, entries)
}

func parseExtentLeafEntries(data []byte, entries uint16) []Extent {
	var extents []Extent
	for i := uint16(0); i < entries && int(12+i*12+12) <= len(data); i++ {
		off := 12 + i*12
		ext := Extent{
			Block:   binary.LittleEndian.Uint32(data[off : off+4]),
			Length:  binary.LittleEndian.Uint16(data[off+4 : off+6]),
			StartHi: binary.LittleEndian.Uint16(data[off+6 : off+8]),
			StartLo: binary.LittleEndian.Uint32(data[off+8 : off+12]),
		}
		extents = append(extents, ext)
	}

	return extents
}
