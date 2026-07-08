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

	// Inode field offsets within the on-disk inode structure
	inodeOffMode         = 0x00
	inodeOffSizeLo       = 0x04
	inodeOffDeletionTime = 0x14
	inodeOffLinksCount   = 0x1A
	inodeOffBlocksLo     = 0x1C
	inodeOffFlags        = 0x20
	inodeOffBlockArea    = 0x28 // Start of block pointers or extent tree (60 bytes)
	inodeOffBlockAreaEnd = 0x64 // End of block area
	inodeOffSizeHi       = 0x6C

	// Superblock field offsets (relative to start of superblock)
	sbOffInodesCount    = 0x00
	sbOffBlocksCountLo  = 0x04
	sbOffFirstDataBlock = 0x14
	sbOffLogBlockSize   = 0x18
	sbOffBlocksPerGroup = 0x20
	sbOffInodesPerGroup = 0x28
	sbOffMagic          = 0x38
	sbOffInodeSize      = 0x58
	sbOffFeatureIncompat = 0x60
	sbOffJournalInode   = 0xE0
	sbOffGroupDescSize  = 0xFE
	sbOffBlocksCountHi  = 0x150

	// Journal (JBD2) constants
	journalMagic = 0xC03B3998
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
	JournalInode   uint32
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
	Number                 uint32
	Mode                   uint16
	Size                   int64
	Blocks                 uint64
	Flags                  uint32
	LinkCount              uint16
	DeletionTime           uint32
	Extents                []Extent
	ExtentRoot             []byte
	BlockPtrs              [15]uint32
	NeedsJournalRecovery   bool
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

	magic := binary.LittleEndian.Uint16(buf[sbOffMagic : sbOffMagic+2])
	if magic != SuperblockMagic {
		return fmt.Errorf("not an ext4 filesystem (magic: 0x%04X, expected 0x%04X)", magic, SuperblockMagic)
	}

	logBlockSize := binary.LittleEndian.Uint32(buf[sbOffLogBlockSize : sbOffLogBlockSize+4])

	p.sb = &Superblock{
		TotalInodes:    binary.LittleEndian.Uint32(buf[sbOffInodesCount : sbOffInodesCount+4]),
		BlockSize:      1024 << logBlockSize,
		BlocksPerGroup: binary.LittleEndian.Uint32(buf[sbOffBlocksPerGroup : sbOffBlocksPerGroup+4]),
		InodesPerGroup: binary.LittleEndian.Uint32(buf[sbOffInodesPerGroup : sbOffInodesPerGroup+4]),
		InodeSize:      binary.LittleEndian.Uint16(buf[sbOffInodeSize : sbOffInodeSize+2]),
		Magic:          magic,
		FirstDataBlock: binary.LittleEndian.Uint32(buf[sbOffFirstDataBlock : sbOffFirstDataBlock+4]),
		GroupDescSize:  binary.LittleEndian.Uint16(buf[sbOffGroupDescSize : sbOffGroupDescSize+2]),
	}

	// Total blocks (handle 64-bit)
	blocksLo := binary.LittleEndian.Uint32(buf[sbOffBlocksCountLo : sbOffBlocksCountLo+4])
	blocksHi := binary.LittleEndian.Uint32(buf[sbOffBlocksCountHi : sbOffBlocksCountHi+4])
	p.sb.TotalBlocks = uint64(blocksHi)<<32 | uint64(blocksLo)

	// Check for 64-bit feature
	featureIncompat := binary.LittleEndian.Uint32(buf[sbOffFeatureIncompat : sbOffFeatureIncompat+4])
	p.sb.Feature64bit = featureIncompat&0x80 != 0

	// Journal inode number
	p.sb.JournalInode = binary.LittleEndian.Uint32(buf[sbOffJournalInode : sbOffJournalInode+4])
	if p.sb.JournalInode == 0 {
		p.sb.JournalInode = 8 // Default journal inode
	}

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
		Mode:         binary.LittleEndian.Uint16(buf[inodeOffMode : inodeOffMode+2]),
		LinkCount:    binary.LittleEndian.Uint16(buf[inodeOffLinksCount : inodeOffLinksCount+2]),
		Flags:        binary.LittleEndian.Uint32(buf[inodeOffFlags : inodeOffFlags+4]),
		DeletionTime: binary.LittleEndian.Uint32(buf[inodeOffDeletionTime : inodeOffDeletionTime+4]),
	}

	// Size (combine low and high for large files)
	sizeLo := binary.LittleEndian.Uint32(buf[inodeOffSizeLo : inodeOffSizeLo+4])
	sizeHi := binary.LittleEndian.Uint32(buf[inodeOffSizeHi : inodeOffSizeHi+4])
	inode.Size = int64(sizeHi)<<32 | int64(sizeLo)

	// Block count
	blocksLo := binary.LittleEndian.Uint32(buf[inodeOffBlocksLo : inodeOffBlocksLo+4])
	inode.Blocks = uint64(blocksLo)

	// Parse block pointers or extents (60-byte area at offset 0x28)
	if inode.Flags&InodeFlagExtents != 0 {
		inode.ExtentRoot = append([]byte(nil), buf[inodeOffBlockArea:inodeOffBlockAreaEnd]...)
		inode.Extents = parseExtents(inode.ExtentRoot)
	} else {
		for i := 0; i < 15; i++ {
			off := inodeOffBlockArea + i*4
			inode.BlockPtrs[i] = binary.LittleEndian.Uint32(buf[off : off+4])
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
				// Check if extent/block data has been zeroed by the kernel
				inode.NeedsJournalRecovery = p.hasZeroedBlockData(inode)
				callback(inode)
			}
		}
	}

	return nil
}

// hasZeroedBlockData checks if an inode's extent tree or block pointers are all zeros,
// indicating the kernel zeroed them on deletion and journal recovery is needed.
func (p *Ext4Parser) hasZeroedBlockData(inode *Inode) bool {
	if inode.Flags&InodeFlagExtents != 0 {
		// For extent-based inodes, check if the extent root area is all zeros
		// or has no valid extents
		if len(inode.ExtentRoot) == 0 {
			return true
		}
		allZero := true
		for _, b := range inode.ExtentRoot {
			if b != 0 {
				allZero = false
				break
			}
		}
		if allZero {
			return true
		}
		// Check if extent magic is missing (kernel zeroed the tree)
		if len(inode.ExtentRoot) >= 2 {
			magic := binary.LittleEndian.Uint16(inode.ExtentRoot[0:2])
			if magic != 0xF30A {
				return true
			}
		}
		// Check if all extents point to block 0
		if len(inode.Extents) == 0 {
			return true
		}
		for _, ext := range inode.Extents {
			if ext.PhysicalBlock() != 0 {
				return false
			}
		}
		return true
	}

	// For legacy block pointer inodes, check if all pointers are zero
	for _, ptr := range inode.BlockPtrs {
		if ptr != 0 {
			return false
		}
	}
	return true
}

// ScanJournalForInodes reads the ext4 journal (jbd2) and scans for old copies
// of deleted inodes that still have intact extent trees or block pointers.
// The kernel zeroes extent/block data on deletion, but the journal retains
// pre-deletion copies that can be used for data recovery.
func (p *Ext4Parser) ScanJournalForInodes(callback func(inode *Inode)) error {
	if p.sb == nil {
		return fmt.Errorf("superblock not parsed")
	}

	journalInodeNum := p.sb.JournalInode
	if journalInodeNum == 0 {
		journalInodeNum = 8
	}

	// Read the journal inode itself
	journalInode, err := p.ReadInode(journalInodeNum)
	if err != nil {
		return fmt.Errorf("failed to read journal inode %d: %w", journalInodeNum, err)
	}

	// Read journal data
	journalData, err := p.RecoverInodeData(journalInode)
	if err != nil {
		return fmt.Errorf("failed to read journal data: %w", err)
	}

	if len(journalData) == 0 {
		return fmt.Errorf("journal data is empty")
	}

	// Validate journal superblock magic (0xC03B3998 at offset 0, big-endian)
	if len(journalData) >= 4 {
		jsMagic := binary.BigEndian.Uint32(journalData[0:4])
		if jsMagic != 0xC03B3998 {
			return fmt.Errorf("invalid journal superblock magic: 0x%08X", jsMagic)
		}
	}

	inodeSize := int(p.sb.InodeSize)
	if inodeSize == 0 {
		inodeSize = 256
	}

	// Track best inode per inode number (most recent DeletionTime wins)
	type journalInodeEntry struct {
		inode        *Inode
		deletionTime uint32
	}
	found := make(map[uint32]*journalInodeEntry)

	// Scan journal data at inode-size aligned offsets looking for deleted inode records
	for off := 0; off+inodeSize <= len(journalData); off += inodeSize {
		record := journalData[off : off+inodeSize]

		// Check mode field (offset 0x00): must have regular file type bits
		mode := binary.LittleEndian.Uint16(record[0x00:0x02])
		if mode&0xF000 != InodeModeRegFile {
			continue
		}

		// Check DeletionTime (offset 0x14): must be non-zero
		deletionTime := binary.LittleEndian.Uint32(record[0x14:0x18])
		if deletionTime == 0 {
			continue
		}

		// Check Size > 0 (low at 0x04, high at 0x6C)
		sizeLo := binary.LittleEndian.Uint32(record[0x04:0x08])
		var sizeHi uint32
		if inodeSize > 0x70 {
			sizeHi = binary.LittleEndian.Uint32(record[0x6C:0x70])
		}
		size := int64(sizeHi)<<32 | int64(sizeLo)
		if size <= 0 {
			continue
		}

		// Check for usable extent/block pointer data at offset 0x28
		blockArea := record[0x28:0x64]
		hasExtentMagic := binary.LittleEndian.Uint16(blockArea[0:2]) == 0xF30A
		hasNonZeroBlocks := false

		if !hasExtentMagic {
			// Check if any block pointer in the 60-byte area is non-zero
			for i := 0; i+4 <= len(blockArea); i += 4 {
				if binary.LittleEndian.Uint32(blockArea[i:i+4]) != 0 {
					hasNonZeroBlocks = true
					break
				}
			}
		}

		if !hasExtentMagic && !hasNonZeroBlocks {
			// No usable block/extent data
			continue
		}

		// Parse the flags
		flags := binary.LittleEndian.Uint32(record[0x20:0x24])
		linkCount := binary.LittleEndian.Uint16(record[0x1A:0x1C])
		blocksLo := binary.LittleEndian.Uint32(record[0x1C:0x20])

		// Build the inode
		inode := &Inode{
			Mode:         mode,
			Size:         size,
			Flags:        flags,
			LinkCount:    linkCount,
			DeletionTime: deletionTime,
			Blocks:       uint64(blocksLo),
		}

		if flags&InodeFlagExtents != 0 && hasExtentMagic {
			inode.ExtentRoot = append([]byte(nil), blockArea...)
			inode.Extents = parseExtents(inode.ExtentRoot)
		} else if hasNonZeroBlocks {
			for i := 0; i < 15 && (i*4+4) <= len(blockArea); i++ {
				inode.BlockPtrs[i] = binary.LittleEndian.Uint32(blockArea[i*4 : i*4+4])
			}
		}

		// We cannot determine the exact inode number from journal data alone,
		// so we use a hash of the key fields to deduplicate.
		// Attempt to identify by size + deletion time as a dedup key.
		// A better approach: try to match against known deleted inodes.
		// For now, use offset-based numbering as a synthetic inode number.
		syntheticNum := uint32(off / inodeSize)
		inode.Number = syntheticNum

		existing, ok := found[syntheticNum]
		if !ok || deletionTime > existing.deletionTime {
			found[syntheticNum] = &journalInodeEntry{
				inode:        inode,
				deletionTime: deletionTime,
			}
		}
	}

	// Also scan at block-size aligned offsets to catch inodes that appear
	// at the start of journal data blocks (more likely positions)
	blockSize := int(p.sb.BlockSize)
	if blockSize > 0 {
		for off := blockSize; off+inodeSize <= len(journalData); off += blockSize {
			// Try each inode-sized slot within this block
			for slot := 0; slot+inodeSize <= blockSize; slot += inodeSize {
				pos := off + slot
				if pos+inodeSize > len(journalData) {
					break
				}
				record := journalData[pos : pos+inodeSize]

				mode := binary.LittleEndian.Uint16(record[0x00:0x02])
				if mode&0xF000 != InodeModeRegFile {
					continue
				}

				deletionTime := binary.LittleEndian.Uint32(record[0x14:0x18])
				if deletionTime == 0 {
					continue
				}

				sizeLo := binary.LittleEndian.Uint32(record[0x04:0x08])
				var sizeHi uint32
				if inodeSize > 0x70 {
					sizeHi = binary.LittleEndian.Uint32(record[0x6C:0x70])
				}
				size := int64(sizeHi)<<32 | int64(sizeLo)
				if size <= 0 {
					continue
				}

				blockArea := record[0x28:0x64]
				hasExtentMagic := binary.LittleEndian.Uint16(blockArea[0:2]) == 0xF30A
				hasNonZeroBlocks := false
				if !hasExtentMagic {
					for i := 0; i+4 <= len(blockArea); i += 4 {
						if binary.LittleEndian.Uint32(blockArea[i:i+4]) != 0 {
							hasNonZeroBlocks = true
							break
						}
					}
				}
				if !hasExtentMagic && !hasNonZeroBlocks {
					continue
				}

				flags := binary.LittleEndian.Uint32(record[0x20:0x24])
				linkCount := binary.LittleEndian.Uint16(record[0x1A:0x1C])
				blocksLo := binary.LittleEndian.Uint32(record[0x1C:0x20])

				inode := &Inode{
					Mode:         mode,
					Size:         size,
					Flags:        flags,
					LinkCount:    linkCount,
					DeletionTime: deletionTime,
					Blocks:       uint64(blocksLo),
				}

				if flags&InodeFlagExtents != 0 && hasExtentMagic {
					inode.ExtentRoot = append([]byte(nil), blockArea...)
					inode.Extents = parseExtents(inode.ExtentRoot)
				} else if hasNonZeroBlocks {
					for i := 0; i < 15 && (i*4+4) <= len(blockArea); i++ {
						inode.BlockPtrs[i] = binary.LittleEndian.Uint32(blockArea[i*4 : i*4+4])
					}
				}

				syntheticNum := uint32(pos / inodeSize)
				inode.Number = syntheticNum

				existing, ok := found[syntheticNum]
				if !ok || deletionTime > existing.deletionTime {
					found[syntheticNum] = &journalInodeEntry{
						inode:        inode,
						deletionTime: deletionTime,
					}
				}
			}
		}
	}

	// Deliver all found inodes to the callback
	for _, entry := range found {
		callback(entry.inode)
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
