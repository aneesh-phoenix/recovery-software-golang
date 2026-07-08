package output

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWriteReport_CreatesValidJSON(t *testing.T) {
	dir := t.TempDir()
	report := &RecoveryReport{
		Timestamp:    time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC),
		Source:       "/dev/sda1",
		OutputDir:    dir,
		TotalScanned: 1048576,
		FilesFound:   2,
		FilesByType:  map[string]int{"pdf": 1, "zip": 1},
		Entries: []ReportEntry{
			{FileName: "file1.pdf", FileType: "pdf", Size: 4096, DiskOffset: 512, Source: "carver"},
			{FileName: "file2.zip", FileType: "zip", Size: 8192, DiskOffset: 65536, Source: "ntfs_mft"},
		},
	}

	err := WriteReport(report, dir)
	if err != nil {
		t.Fatalf("WriteReport returned error: %v", err)
	}

	reportPath := filepath.Join(dir, "recovery_report.json")
	data, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatalf("failed to read report file: %v", err)
	}

	if !json.Valid(data) {
		t.Error("report file does not contain valid JSON")
	}
}

func TestWriteReport_ParseBack(t *testing.T) {
	dir := t.TempDir()
	original := &RecoveryReport{
		Timestamp:    time.Date(2026, 1, 15, 8, 30, 0, 0, time.UTC),
		Source:       "/dev/sdb2",
		OutputDir:    "/recovered",
		TotalScanned: 5242880,
		FilesFound:   3,
		FilesByType:  map[string]int{"pdf": 2, "docx": 1},
		Entries: []ReportEntry{
			{FileName: "report.pdf", FileType: "pdf", Size: 10240, DiskOffset: 1024, Source: "carver"},
			{FileName: "notes.pdf", FileType: "pdf", Size: 2048, DiskOffset: 20480, Source: "ext4_inode"},
			{FileName: "doc.docx", FileType: "docx", Size: 51200, DiskOffset: 102400, Source: "ntfs_mft"},
		},
	}

	err := WriteReport(original, dir)
	if err != nil {
		t.Fatalf("WriteReport returned error: %v", err)
	}

	reportPath := filepath.Join(dir, "recovery_report.json")
	data, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatalf("failed to read report file: %v", err)
	}

	var parsed RecoveryReport
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal report: %v", err)
	}

	if !parsed.Timestamp.Equal(original.Timestamp) {
		t.Errorf("Timestamp mismatch: got %v, want %v", parsed.Timestamp, original.Timestamp)
	}
	if parsed.Source != original.Source {
		t.Errorf("Source mismatch: got %q, want %q", parsed.Source, original.Source)
	}
	if parsed.OutputDir != original.OutputDir {
		t.Errorf("OutputDir mismatch: got %q, want %q", parsed.OutputDir, original.OutputDir)
	}
	if parsed.TotalScanned != original.TotalScanned {
		t.Errorf("TotalScanned mismatch: got %d, want %d", parsed.TotalScanned, original.TotalScanned)
	}
	if parsed.FilesFound != original.FilesFound {
		t.Errorf("FilesFound mismatch: got %d, want %d", parsed.FilesFound, original.FilesFound)
	}
	if len(parsed.FilesByType) != len(original.FilesByType) {
		t.Errorf("FilesByType length mismatch: got %d, want %d", len(parsed.FilesByType), len(original.FilesByType))
	}
	for k, v := range original.FilesByType {
		if parsed.FilesByType[k] != v {
			t.Errorf("FilesByType[%q] mismatch: got %d, want %d", k, parsed.FilesByType[k], v)
		}
	}
	if len(parsed.Entries) != len(original.Entries) {
		t.Fatalf("Entries length mismatch: got %d, want %d", len(parsed.Entries), len(original.Entries))
	}
	for i, entry := range original.Entries {
		if parsed.Entries[i].FileName != entry.FileName {
			t.Errorf("Entries[%d].FileName mismatch: got %q, want %q", i, parsed.Entries[i].FileName, entry.FileName)
		}
		if parsed.Entries[i].FileType != entry.FileType {
			t.Errorf("Entries[%d].FileType mismatch: got %q, want %q", i, parsed.Entries[i].FileType, entry.FileType)
		}
		if parsed.Entries[i].Size != entry.Size {
			t.Errorf("Entries[%d].Size mismatch: got %d, want %d", i, parsed.Entries[i].Size, entry.Size)
		}
		if parsed.Entries[i].DiskOffset != entry.DiskOffset {
			t.Errorf("Entries[%d].DiskOffset mismatch: got %d, want %d", i, parsed.Entries[i].DiskOffset, entry.DiskOffset)
		}
		if parsed.Entries[i].Source != entry.Source {
			t.Errorf("Entries[%d].Source mismatch: got %q, want %q", i, parsed.Entries[i].Source, entry.Source)
		}
	}
}

func TestWriteReport_EmptyReport(t *testing.T) {
	dir := t.TempDir()
	report := &RecoveryReport{
		Timestamp:    time.Time{},
		Source:       "",
		OutputDir:    "",
		TotalScanned: 0,
		FilesFound:   0,
		FilesByType:  nil,
		Entries:      nil,
	}

	err := WriteReport(report, dir)
	if err != nil {
		t.Fatalf("WriteReport returned error for empty report: %v", err)
	}

	reportPath := filepath.Join(dir, "recovery_report.json")
	data, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatalf("failed to read report file: %v", err)
	}

	if !json.Valid(data) {
		t.Error("empty report file does not contain valid JSON")
	}

	var parsed RecoveryReport
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal empty report: %v", err)
	}

	if parsed.FilesFound != 0 {
		t.Errorf("expected FilesFound 0, got %d", parsed.FilesFound)
	}
	if parsed.TotalScanned != 0 {
		t.Errorf("expected TotalScanned 0, got %d", parsed.TotalScanned)
	}
}

func TestWriteReport_NonexistentDirectory(t *testing.T) {
	report := &RecoveryReport{
		Timestamp:    time.Now(),
		Source:       "/dev/sda",
		OutputDir:    "/nonexistent",
		TotalScanned: 100,
		FilesFound:   1,
		FilesByType:  map[string]int{"pdf": 1},
		Entries: []ReportEntry{
			{FileName: "test.pdf", FileType: "pdf", Size: 100, DiskOffset: 0, Source: "carver"},
		},
	}

	err := WriteReport(report, "/nonexistent/path/that/does/not/exist")
	if err == nil {
		t.Error("expected error when writing to nonexistent directory, got nil")
	}
}

func TestWriteReport_MultipleEntriesAndTypes(t *testing.T) {
	dir := t.TempDir()
	report := &RecoveryReport{
		Timestamp:    time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC),
		Source:       "/dev/nvme0n1p2",
		OutputDir:    dir,
		TotalScanned: 107374182400, // 100 GB
		FilesFound:   5,
		FilesByType: map[string]int{
			"pdf":  2,
			"zip":  1,
			"docx": 1,
			"xlsx": 1,
		},
		Entries: []ReportEntry{
			{FileName: "document.pdf", FileType: "pdf", Size: 1048576, DiskOffset: 0, Source: "ntfs_mft"},
			{FileName: "scan.pdf", FileType: "pdf", Size: 524288, DiskOffset: 2097152, Source: "carver"},
			{FileName: "archive.zip", FileType: "zip", Size: 10485760, DiskOffset: 4194304, Source: "ext4_inode"},
			{FileName: "report.docx", FileType: "docx", Size: 204800, DiskOffset: 15728640, Source: "ntfs_mft"},
			{FileName: "data.xlsx", FileType: "xlsx", Size: 409600, DiskOffset: 20971520, Source: "carver"},
		},
	}

	err := WriteReport(report, dir)
	if err != nil {
		t.Fatalf("WriteReport returned error: %v", err)
	}

	reportPath := filepath.Join(dir, "recovery_report.json")
	data, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatalf("failed to read report file: %v", err)
	}

	var parsed RecoveryReport
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal report: %v", err)
	}

	if parsed.FilesFound != 5 {
		t.Errorf("FilesFound: got %d, want 5", parsed.FilesFound)
	}
	if len(parsed.FilesByType) != 4 {
		t.Errorf("FilesByType length: got %d, want 4", len(parsed.FilesByType))
	}
	if len(parsed.Entries) != 5 {
		t.Errorf("Entries length: got %d, want 5", len(parsed.Entries))
	}
	if parsed.TotalScanned != 107374182400 {
		t.Errorf("TotalScanned: got %d, want 107374182400", parsed.TotalScanned)
	}
}

func TestFormatSize_ZeroBytes(t *testing.T) {
	result := formatSize(0)
	expected := "0 bytes"
	if result != expected {
		t.Errorf("formatSize(0) = %q, want %q", result, expected)
	}
}

func TestFormatSize_BytesRange(t *testing.T) {
	tests := []struct {
		input    int64
		expected string
	}{
		{1, "1 bytes"},
		{512, "512 bytes"},
		{1023, "1023 bytes"},
	}

	for _, tc := range tests {
		result := formatSize(tc.input)
		if result != tc.expected {
			t.Errorf("formatSize(%d) = %q, want %q", tc.input, result, tc.expected)
		}
	}
}

func TestFormatSize_KBRange(t *testing.T) {
	tests := []struct {
		input    int64
		expected string
	}{
		{2048, "2.00 KB"},
		{5120, "5.00 KB"},
		{512000, "500.00 KB"},
	}

	for _, tc := range tests {
		result := formatSize(tc.input)
		if result != tc.expected {
			t.Errorf("formatSize(%d) = %q, want %q", tc.input, result, tc.expected)
		}
	}
}

func TestFormatSize_MBRange(t *testing.T) {
	tests := []struct {
		input    int64
		expected string
	}{
		{1048576 * 2, "2.00 MB"},
		{1048576 * 50, "50.00 MB"},
		{1048576 * 500, "500.00 MB"},
	}

	for _, tc := range tests {
		result := formatSize(tc.input)
		if result != tc.expected {
			t.Errorf("formatSize(%d) = %q, want %q", tc.input, result, tc.expected)
		}
	}
}

func TestFormatSize_GBRange(t *testing.T) {
	tests := []struct {
		input    int64
		expected string
	}{
		{1073741824 * 2, "2.00 GB"},
		{1073741824 * 10, "10.00 GB"},
		{1073741824 * 100, "100.00 GB"},
	}

	for _, tc := range tests {
		result := formatSize(tc.input)
		if result != tc.expected {
			t.Errorf("formatSize(%d) = %q, want %q", tc.input, result, tc.expected)
		}
	}
}

func TestFormatSize_BoundaryValues(t *testing.T) {
	tests := []struct {
		name     string
		input    int64
		expected string
	}{
		{"exactly 1 KB", 1024, "1.00 KB"},
		{"exactly 1 MB", 1048576, "1.00 MB"},
		{"exactly 1 GB", 1073741824, "1.00 GB"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := formatSize(tc.input)
			if result != tc.expected {
				t.Errorf("formatSize(%d) = %q, want %q", tc.input, result, tc.expected)
			}
		})
	}
}

func TestPrintSummary_NoPanic(t *testing.T) {
	reports := []*RecoveryReport{
		{
			Timestamp:    time.Now(),
			Source:       "/dev/sda",
			OutputDir:    "/tmp/recovered",
			TotalScanned: 1073741824,
			FilesFound:   3,
			FilesByType:  map[string]int{"pdf": 2, "zip": 1},
			Entries: []ReportEntry{
				{FileName: "a.pdf", FileType: "pdf", Size: 1024, DiskOffset: 0, Source: "carver"},
			},
		},
		{
			// Empty report
			FilesByType: map[string]int{},
			Entries:     nil,
		},
		{
			// Nil map
			FilesByType: nil,
			Entries:     nil,
		},
	}

	for i, report := range reports {
		t.Run(fmt.Sprintf("report_%d", i), func(t *testing.T) {
			// Redirect stdout to discard output noise during testing
			oldStdout := os.Stdout
			devNull, err := os.Open(os.DevNull)
			if err != nil {
				t.Fatalf("failed to open devnull: %v", err)
			}
			os.Stdout = devNull
			defer func() {
				os.Stdout = oldStdout
				devNull.Close()
			}()

			// The main assertion: PrintSummary must not panic
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("PrintSummary panicked with report %d: %v", i, r)
				}
			}()

			PrintSummary(report)
		})
	}
}
