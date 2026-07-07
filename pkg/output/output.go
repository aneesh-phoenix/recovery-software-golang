// Package output handles writing recovered files and generating reports.
package output

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// RecoveryReport summarizes a recovery session.
type RecoveryReport struct {
	Timestamp    time.Time      `json:"timestamp"`
	Source       string         `json:"source"`
	OutputDir    string         `json:"output_dir"`
	TotalScanned int64          `json:"total_bytes_scanned"`
	FilesFound   int            `json:"files_found"`
	FilesByType  map[string]int `json:"files_by_type"`
	Entries      []ReportEntry  `json:"entries"`
}

// ReportEntry represents a single recovered file in the report.
type ReportEntry struct {
	FileName   string `json:"filename"`
	FileType   string `json:"file_type"`
	Size       int64  `json:"size_bytes"`
	DiskOffset int64  `json:"disk_offset"`
	Source     string `json:"recovery_source"` // "carver", "ntfs_mft", "ext4_inode"
}

// WriteReport saves the recovery report as JSON.
func WriteReport(report *RecoveryReport, outputDir string) error {
	reportPath := filepath.Join(outputDir, "recovery_report.json")
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal report: %w", err)
	}
	return os.WriteFile(reportPath, data, 0o644)
}

// PrintSummary prints a human-readable recovery summary to stdout.
func PrintSummary(report *RecoveryReport) {
	fmt.Println("\n╔══════════════════════════════════════════╗")
	fmt.Println("║         RECOVERY SUMMARY                 ║")
	fmt.Println("╠══════════════════════════════════════════╣")
	fmt.Printf("║ Source:       %s\n", report.Source)
	fmt.Printf("║ Output:       %s\n", report.OutputDir)
	fmt.Printf("║ Scanned:      %s\n", formatSize(report.TotalScanned))
	fmt.Printf("║ Files Found:  %d\n", report.FilesFound)
	fmt.Println("╠══════════════════════════════════════════╣")
	fmt.Println("║ Breakdown by type:")
	for ftype, count := range report.FilesByType {
		fmt.Printf("║   %-10s  %d files\n", ftype, count)
	}
	fmt.Println("╚══════════════════════════════════════════╝")
}

func formatSize(bytes int64) string {
	const (
		KB = 1024
		MB = KB * 1024
		GB = MB * 1024
	)
	switch {
	case bytes >= GB:
		return fmt.Sprintf("%.2f GB", float64(bytes)/float64(GB))
	case bytes >= MB:
		return fmt.Sprintf("%.2f MB", float64(bytes)/float64(MB))
	case bytes >= KB:
		return fmt.Sprintf("%.2f KB", float64(bytes)/float64(KB))
	default:
		return fmt.Sprintf("%d bytes", bytes)
	}
}
