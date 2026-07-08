// Package ntfs provides NTFS filesystem awareness for file recovery.
// It parses the MFT (Master File Table) to recover file metadata
// including original filenames, paths, and data run locations.
package ntfs

import (
	"encoding/binary"
	"fmt"
	"unicode/utf16"

	"github.com/aneesh/recovery-soft/pkg/scanner"
)

// NTFS constants
const (
	MFTEntrySize      = 1024
	MFTSignature      = "FILE"
	AttrFileName      = 0x30
	AttrData          = 0x80
	AttrEnd           = 0xFFFFFFFF
	AttrIndexRoot     = 0x90
	AttrIndexAlloc    = 0xA0

	// Boot sector field offsets
	bootOffSignature        = 0x03 // "NTFS" (4 bytes)
	bootOffBytesPerSector   = 0x0B // uint16 LE
	bootOffSectorsPerCluster = 0x0D // uint8
	bootOffMFTCluster       = 0x30 // uint64 LE
	bootOffMFTMirrCluster   = 0x38 // uint64 LE

	// MFT entry header offsets
	mftOffFixupOffset    = 0x04 // uint16 LE
	mftOffFixupCount     = 0x06 // uint16 LE
	mftOffFirstAttr      = 0x14 // uint16 LE — offset to first attribute
	mftOffFlags          = 0x16 // uint16 LE — bit 0: in use, bit 1: directory
	mftOffRecordNumber   = 0x2C // uint32 LE

	// $FILE_NAME attribute content offsets (relative to content start)
	fnOffParentRef = 0x00 // uint64 LE (lower 48 bits = parent MFT ref)
	fnOffNameLen   = 0x40 // uint8 — character count
	fnOffNamespace = 0x41 // uint8 — 0=POSIX, 1=Win32, 2=DOS, 3=Win32+DOS
	fnOffName      = 0x42 // UTF-16LE name starts here
)

// BootSector represents key fields from the NTFS boot sector.
type BootSector struct {
	BytesPerSector    uint16
	SectorsPerCluster uint8
	MFTCluster        int64
	MFTMirrCluster    int64
	ClusterSize       int64
}

// MFTEntry represents a parsed MFT record.
type MFTEntry struct {
	RecordNumber uint32
	InUse        bool
	IsDirectory  bool
	FileName     string
	ParentRef    uint64
	FileSize     int64
	DataRuns     []DataRun
	Offset       int64 // Offset on disk where this entry was found
}

// DataRun represents a contiguous run of clusters containing file data.
type DataRun struct {
	ClusterOffset int64 // Starting cluster (absolute)
	ClusterCount  int64 // Number of clusters in this run
}

// NTFSParser provides NTFS filesystem parsing capabilities.
type NTFSParser struct {
	reader    *scanner.DiskReader
	boot      *BootSector
	partition int64 // Partition start offset
}

// NewParser creates a new NTFS parser for the given disk reader.
// partitionOffset is the byte offset where the NTFS partition starts.
func NewParser(reader *scanner.DiskReader, partitionOffset int64) (*NTFSParser, error) {
	return &NTFSParser{
		reader:    reader,
		partition: partitionOffset,
	}, nil
}

// ParseBootSector reads and parses the NTFS boot sector.
func (p *NTFSParser) ParseBootSector() error {
	buf := make([]byte, 512)
	_, err := p.reader.ReadAt(buf, p.partition)
	if err != nil {
		return fmt.Errorf("failed to read boot sector: %w", err)
	}

	// Verify NTFS signature
	if string(buf[bootOffSignature:bootOffSignature+4]) != "NTFS" {
		return fmt.Errorf("not an NTFS filesystem (signature: %q)", string(buf[bootOffSignature:bootOffSignature+4]))
	}

	p.boot = &BootSector{
		BytesPerSector:    binary.LittleEndian.Uint16(buf[bootOffBytesPerSector : bootOffBytesPerSector+2]),
		SectorsPerCluster: buf[bootOffSectorsPerCluster],
	}
	p.boot.ClusterSize = int64(p.boot.BytesPerSector) * int64(p.boot.SectorsPerCluster)
	p.boot.MFTCluster = int64(binary.LittleEndian.Uint64(buf[bootOffMFTCluster : bootOffMFTCluster+8]))
	p.boot.MFTMirrCluster = int64(binary.LittleEndian.Uint64(buf[bootOffMFTMirrCluster : bootOffMFTMirrCluster+8]))

	return nil
}

// BootInfo returns the parsed boot sector information.
func (p *NTFSParser) BootInfo() *BootSector {
	return p.boot
}

// MFTOffset returns the byte offset of the MFT on disk.
func (p *NTFSParser) MFTOffset() int64 {
	if p.boot == nil {
		return 0
	}
	return p.partition + p.boot.MFTCluster*p.boot.ClusterSize
}

// ReadMFTEntry reads and parses a single MFT entry at the given offset.
func (p *NTFSParser) ReadMFTEntry(offset int64) (*MFTEntry, error) {
	buf := make([]byte, MFTEntrySize)
	_, err := p.reader.ReadAt(buf, offset)
	if err != nil {
		return nil, fmt.Errorf("failed to read MFT entry at %d: %w", offset, err)
	}

	// Check FILE signature
	if string(buf[0:4]) != MFTSignature {
		return nil, fmt.Errorf("invalid MFT entry signature at offset %d", offset)
	}

	entry := &MFTEntry{Offset: offset}

	// Parse MFT entry header
	flags := binary.LittleEndian.Uint16(buf[mftOffFlags : mftOffFlags+2])
	entry.InUse = flags&0x01 != 0
	entry.IsDirectory = flags&0x02 != 0
	entry.RecordNumber = binary.LittleEndian.Uint32(buf[mftOffRecordNumber : mftOffRecordNumber+4])

	// Apply fixup array to correct sector-end bytes
	fixupOffset := binary.LittleEndian.Uint16(buf[mftOffFixupOffset : mftOffFixupOffset+2])
	fixupCount := binary.LittleEndian.Uint16(buf[mftOffFixupCount : mftOffFixupCount+2])
	if err := applyFixup(buf, int(fixupOffset), int(fixupCount)); err != nil {
		// Non-fatal, continue parsing
		_ = err
	}

	// Walk attribute list starting from first attribute offset
	attrOffset := int(binary.LittleEndian.Uint16(buf[mftOffFirstAttr : mftOffFirstAttr+2]))
	for attrOffset < MFTEntrySize-8 {
		attrType := binary.LittleEndian.Uint32(buf[attrOffset : attrOffset+4])
		if attrType == AttrEnd || attrType == 0 {
			break
		}

		attrLen := int(binary.LittleEndian.Uint32(buf[attrOffset+4 : attrOffset+8]))
		if attrLen <= 0 || attrOffset+attrLen > MFTEntrySize {
			break
		}

		attrData := buf[attrOffset : attrOffset+attrLen]

		switch attrType {
		case AttrFileName:
			name, parentRef := parseFileNameAttr(attrData)
			if name != "" {
				entry.FileName = name
				entry.ParentRef = parentRef
			}
		case AttrData:
			runs, size := parseDataAttr(attrData)
			entry.DataRuns = runs
			entry.FileSize = size
		}

		attrOffset += attrLen
	}

	return entry, nil
}

// ScanMFT scans the MFT and returns all parseable entries.
func (p *NTFSParser) ScanMFT(maxEntries int, callback func(entry *MFTEntry)) error {
	if p.boot == nil {
		return fmt.Errorf("boot sector not parsed, call ParseBootSector first")
	}

	mftStart := p.MFTOffset()
	if maxEntries <= 0 {
		maxEntries = 100000 // Safety limit
	}

	for i := 0; i < maxEntries; i++ {
		offset := mftStart + int64(i)*MFTEntrySize
		if offset >= p.reader.Size() {
			break
		}

		entry, err := p.ReadMFTEntry(offset)
		if err != nil {
			continue // Skip invalid entries
		}

		if callback != nil {
			callback(entry)
		}
	}

	return nil
}

// RecoverFileData reads file data using the data runs from an MFT entry.
func (p *NTFSParser) RecoverFileData(entry *MFTEntry) ([]byte, error) {
	if p.boot == nil {
		return nil, fmt.Errorf("boot sector not parsed")
	}
	if len(entry.DataRuns) == 0 {
		return nil, fmt.Errorf("no data runs for file %q", entry.FileName)
	}

	var data []byte
	for _, run := range entry.DataRuns {
		clusterOffset := p.partition + run.ClusterOffset*p.boot.ClusterSize
		runSize := run.ClusterCount * p.boot.ClusterSize

		buf := make([]byte, runSize)
		n, err := p.reader.ReadAt(buf, clusterOffset)
		if err != nil && n == 0 {
			return nil, fmt.Errorf("failed to read data run: %w", err)
		}
		data = append(data, buf[:n]...)
	}

	// Trim to actual file size
	if entry.FileSize > 0 && int64(len(data)) > entry.FileSize {
		data = data[:entry.FileSize]
	}

	return data, nil
}

// applyFixup corrects sector-end bytes using the fixup array.
func applyFixup(buf []byte, fixupOffset, fixupCount int) error {
	if fixupCount < 2 || fixupOffset+fixupCount*2 > len(buf) {
		return fmt.Errorf("invalid fixup array")
	}
	// First entry is the expected signature
	// Subsequent entries replace the last 2 bytes of each sector
	sectorSize := 512
	for i := 1; i < fixupCount; i++ {
		replaceOffset := i*sectorSize - 2
		if replaceOffset+2 > len(buf) {
			break
		}
		srcOffset := fixupOffset + i*2
		if srcOffset+2 > len(buf) {
			break
		}
		buf[replaceOffset] = buf[srcOffset]
		buf[replaceOffset+1] = buf[srcOffset+1]
	}
	return nil
}

// parseFileNameAttr parses a $FILE_NAME attribute.
func parseFileNameAttr(attrData []byte) (string, uint64) {
	// Check if resident
	nonResident := attrData[8]
	if nonResident != 0 {
		return "", 0
	}

	// Content offset and size for resident attribute
	contentOffset := int(binary.LittleEndian.Uint16(attrData[0x14:0x16]))
	if contentOffset+66 > len(attrData) {
		return "", 0
	}

	content := attrData[contentOffset:]
	if len(content) < 66 {
		return "", 0
	}

	// Parent directory reference (6 bytes) + sequence (2 bytes)
	parentRef := binary.LittleEndian.Uint64(content[0:8]) & 0x0000FFFFFFFFFFFF

	// Filename length at offset 0x40
	nameLen := int(content[0x40])
	// Namespace at offset 0x41 (0=POSIX, 1=Win32, 2=DOS, 3=Win32+DOS)
	namespace := content[0x41]

	if nameLen == 0 || 0x42+nameLen*2 > len(content) {
		return "", parentRef
	}

	// Skip DOS names, prefer Win32 or Win32+DOS
	if namespace == 2 {
		return "", parentRef
	}

	// Decode UTF-16LE filename
	nameBytes := content[0x42 : 0x42+nameLen*2]
	name := decodeUTF16(nameBytes)

	return name, parentRef
}

// parseDataAttr parses a $DATA attribute and extracts data runs.
func parseDataAttr(attrData []byte) ([]DataRun, int64) {
	nonResident := attrData[8]
	if nonResident == 0 {
		// Resident data — file content is in the attribute itself
		return nil, 0
	}

	if len(attrData) < 0x40 {
		return nil, 0
	}

	// Real size of the file
	realSize := int64(binary.LittleEndian.Uint64(attrData[0x30:0x38]))

	// Data runs offset
	runOffset := int(binary.LittleEndian.Uint16(attrData[0x20:0x22]))
	if runOffset >= len(attrData) {
		return nil, realSize
	}

	runs := decodeDataRuns(attrData[runOffset:])
	return runs, realSize
}

// decodeDataRuns parses the run-length encoded data run list.
func decodeDataRuns(data []byte) []DataRun {
	var runs []DataRun
	var prevOffset int64
	pos := 0

	for pos < len(data) {
		header := data[pos]
		if header == 0 {
			break
		}

		lengthSize := int(header & 0x0F)
		offsetSize := int(header >> 4)

		pos++
		if pos+lengthSize+offsetSize > len(data) {
			break
		}

		// Read run length (unsigned)
		var runLength int64
		for i := 0; i < lengthSize; i++ {
			runLength |= int64(data[pos+i]) << (uint(i) * 8)
		}
		pos += lengthSize

		// Read run offset (signed, relative to previous)
		var runOffset int64
		if offsetSize > 0 {
			for i := 0; i < offsetSize; i++ {
				runOffset |= int64(data[pos+i]) << (uint(i) * 8)
			}
			// Sign extend
			if data[pos+offsetSize-1]&0x80 != 0 {
				for i := offsetSize; i < 8; i++ {
					runOffset |= int64(0xFF) << (uint(i) * 8)
				}
			}
			pos += offsetSize

			prevOffset += runOffset
			runs = append(runs, DataRun{
				ClusterOffset: prevOffset,
				ClusterCount:  runLength,
			})
		} else {
			// Sparse run (offset = 0 means sparse)
			pos += offsetSize
		}
	}

	return runs
}

// decodeUTF16 converts UTF-16LE bytes to a Go string.
func decodeUTF16(b []byte) string {
	if len(b)%2 != 0 {
		b = b[:len(b)-1]
	}
	u16 := make([]uint16, len(b)/2)
	for i := range u16 {
		u16[i] = binary.LittleEndian.Uint16(b[i*2 : i*2+2])
	}
	return string(utf16.Decode(u16))
}
