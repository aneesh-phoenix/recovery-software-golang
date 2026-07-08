// recovery-soft: A file recovery tool focused on PDF and ZIP files
// with NTFS and ext4 filesystem awareness.
//
// Usage:
//
//	recover carve  <disk/image> <output_dir>    - Raw file carving (signature-based)
//	recover ntfs   <disk/image> <output_dir>    - NTFS-aware recovery using MFT
//	recover ext4   <disk/image> <output_dir>    - ext4-aware recovery using inodes
//	recover auto   <disk/image> <output_dir>    - Auto-detect filesystem and recover
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/aneesh/recovery-soft/pkg/carver"
	"github.com/aneesh/recovery-soft/pkg/ext4"
	"github.com/aneesh/recovery-soft/pkg/ntfs"
	"github.com/aneesh/recovery-soft/pkg/output"
	"github.com/aneesh/recovery-soft/pkg/scanner"
	"github.com/aneesh/recovery-soft/pkg/tui"
)

const version = "0.1.0"

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	command := os.Args[1]

	switch command {
	case "interactive", "i":
		runInteractive()
	case "carve":
		runCarve(os.Args[2:])
	case "ntfs":
		runNTFS(os.Args[2:])
	case "ext4":
		runExt4(os.Args[2:])
	case "auto":
		runAuto(os.Args[2:])
	case "version":
		fmt.Printf("recovery-soft v%s\n", version)
	case "help", "-h", "--help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n\n", command)
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println(`recovery-soft - File recovery tool for PDF and ZIP files

USAGE:
    recover <command> [options] <disk/image> <output_dir>

COMMANDS:
    interactive  Interactive mode — guides you through disk selection (alias: i)
    carve        Raw file carving using signature detection
    ntfs         NTFS-aware recovery (parses MFT for filenames)
    ext4         ext4-aware recovery (parses inodes for deleted files)
    auto         Auto-detect filesystem and use best recovery method
    version      Print version
    help         Show this help

OPTIONS:
    -v           Verbose output
    -offset      Partition offset in bytes (default: 0)
    -block-size  Block size for scanning in bytes (default: 4096)
    -type        File types to recover: pdf, zip, docx, xlsx, pptx (default: all)

EXAMPLES:
    # Interactive mode (recommended for beginners)
    sudo recover interactive

    # Recover from a disk image
    sudo recover carve /dev/sda ./recovered

    # NTFS recovery with verbose output
    sudo recover ntfs -v /dev/sda2 ./recovered

    # Auto-detect and recover from image file
    recover auto disk_backup.img ./recovered

    # Specify partition offset
    recover ntfs -offset 1048576 /dev/sda ./recovered

NOTES:
    - Run with sudo for raw disk access
    - Never write output to the same disk you're recovering from
    - The sooner you run this after data loss, the better`)
}

// ============================================================================
// Recovery Commands
// ============================================================================

// runCarve performs raw file carving — scans disk bytes for known file
// signatures (PDF, ZIP) regardless of filesystem. Works on any disk.
func runCarve(args []string) {
	fs := flag.NewFlagSet("carve", flag.ExitOnError)
	verbose := fs.Bool("v", false, "Verbose output")
	blockSize := fs.Int("block-size", 4096, "Block size for scanning")
	typeFilters := fs.String("type", "", "File types to recover: pdf, zip, docx, xlsx, pptx")
	fs.Parse(args)

	if fs.NArg() < 2 {
		fmt.Fprintln(os.Stderr, "Usage: recover carve [options] <disk/image> <output_dir>")
		os.Exit(1)
	}

	diskPath := fs.Arg(0)
	outputDir := fs.Arg(1)

	fmt.Printf("[*] Starting file carving on %s\n", diskPath)
	fmt.Printf("[*] Output directory: %s\n", outputDir)
	if *typeFilters != "" {
		fmt.Printf("[*] File types: %s\n", *typeFilters)
	}

	c, err := carver.New(carver.Config{
		DiskPath:    diskPath,
		OutputDir:   outputDir,
		BlockSize:   *blockSize,
		TypeFilters: *typeFilters,
		Verbose:     *verbose,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer c.Close()

	fmt.Printf("[*] Disk size: %s\n", formatSize(c.DiskSize()))
	if *typeFilters == "" {
		fmt.Println("[*] Scanning for PDF and ZIP files (ZIPs are classified as DOCX/XLSX/PPTX when applicable)...")
	} else {
		fmt.Println("[*] Scanning selected file types...")
	}

	// Check for TRIM/zeroed blocks
	zeroPercent := c.Reader().SampleForZeroBlocks(4096, 256)
	if zeroPercent > 50 {
		fmt.Printf("[!] WARNING: %.0f%% of sampled disk blocks are zeroed. This indicates SSD TRIM has run.\n", zeroPercent)
		fmt.Println("[!] Recovery chances are significantly reduced. Recently deleted files may be unrecoverable.")
	}

	startTime := time.Now()
	lastProgress := time.Now()

	err = c.Scan(func(offset, total int64) {
		if time.Since(lastProgress) > 2*time.Second {
			pct := float64(offset) / float64(total) * 100
			fmt.Printf("\r[*] Progress: %.1f%% (%s / %s)", pct, formatSize(offset), formatSize(total))
			lastProgress = time.Now()
		}
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "\nError during scan: %v\n", err)
		os.Exit(1)
	}

	elapsed := time.Since(startTime)
	results := c.Results()

	fmt.Printf("\r[*] Scan complete in %s\n", elapsed.Round(time.Second))

	// Generate report
	report := &output.RecoveryReport{
		Timestamp:    time.Now(),
		Source:       diskPath,
		OutputDir:    outputDir,
		TotalScanned: c.DiskSize(),
		FilesFound:   len(results),
		FilesByType:  make(map[string]int),
	}

	for _, r := range results {
		report.FilesByType[r.Signature.Name]++
		report.Entries = append(report.Entries, output.ReportEntry{
			FileName:   r.OutputPath,
			FileType:   r.Signature.Name,
			Size:       r.Size,
			DiskOffset: r.Offset,
			Source:     "carver",
		})
	}

	output.PrintSummary(report)
	if err := output.WriteReport(report, outputDir); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to write report: %v\n", err)
	}
}

// runNTFS recovers files by parsing the NTFS Master File Table (MFT).
// Reads MFT entries to find file names and data run locations.
func runNTFS(args []string) {
	fs := flag.NewFlagSet("ntfs", flag.ExitOnError)
	verbose := fs.Bool("v", false, "Verbose output")
	partOffset := fs.Int64("offset", 0, "Partition offset in bytes")
	maxEntries := fs.Int("max-entries", 50000, "Maximum MFT entries to scan")
	typeFilters := fs.String("type", "", "File types to recover: pdf, zip, docx, xlsx, pptx")
	fs.Parse(args)

	if fs.NArg() < 2 {
		fmt.Fprintln(os.Stderr, "Usage: recover ntfs [options] <disk/image> <output_dir>")
		os.Exit(1)
	}

	diskPath := fs.Arg(0)
	outputDir := fs.Arg(1)
	targetTypes, err := parseTargetFileTypes(*typeFilters)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("[*] NTFS recovery on %s (offset: %d)\n", diskPath, *partOffset)
	if *typeFilters != "" {
		fmt.Printf("[*] File types: %s\n", *typeFilters)
	}

	reader, err := scanner.NewDiskReader(diskPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer reader.Close()

	parser, err := ntfs.NewParser(reader, *partOffset)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("[*] Parsing NTFS boot sector...")
	if err := parser.ParseBootSector(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		fmt.Println("[!] NTFS not detected. Try 'recover carve' for raw recovery.")
		os.Exit(1)
	}

	boot := parser.BootInfo()
	fmt.Printf("[*] Cluster size: %d bytes\n", boot.ClusterSize)
	fmt.Printf("[*] MFT at cluster: %d (offset: 0x%X)\n", boot.MFTCluster, parser.MFTOffset())

	// TRIM detection
	zeroPercent := reader.SampleForZeroBlocks(4096, 256)
	if zeroPercent > 50 {
		fmt.Printf("\n[!] WARNING: %.0f%% of sampled disk blocks are zeroed. This indicates SSD TRIM has run.\n", zeroPercent)
		fmt.Println("[!] Recovery chances are significantly reduced. Recently deleted files may be unrecoverable.")
	}

	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "Error creating output dir: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("[*] Scanning up to %d MFT entries...\n", *maxEntries)

	report := &output.RecoveryReport{
		Timestamp:    time.Now(),
		Source:       diskPath,
		OutputDir:    outputDir,
		TotalScanned: reader.Size(),
		FilesByType:  make(map[string]int),
	}

	count := 0
	err = parser.ScanMFT(*maxEntries, func(entry *ntfs.MFTEntry) {
		// Look for deleted files or files matching our target types
		if entry.FileName == "" || len(entry.DataRuns) == 0 {
			return
		}

		// Check if this is a target file type
		ext := getExtension(entry.FileName)
		if !isTargetFile(ext, targetTypes) {
			return
		}

		if *verbose {
			status := "active"
			if !entry.InUse {
				status = "DELETED"
			}
			fmt.Printf("[+] Found %s: %s (%s, %d bytes)\n",
				status, entry.FileName, ext, entry.FileSize)
		}

		// Attempt to recover file data
		data, err := parser.RecoverFileData(entry)
		if err != nil {
			if *verbose {
				fmt.Printf("[-] Failed to recover %s: %v\n", entry.FileName, err)
			}
			return
		}

		if len(data) == 0 {
			return
		}

		// Validate recovered file content before saving
		if !carver.ValidateRecoveredFile(data, ext, *verbose) {
			if *verbose {
				fmt.Printf("[-] Rejected %s: content validation failed\n", entry.FileName)
			}
			return
		}

		// Save recovered file
		count++
		outPath := fmt.Sprintf("%s/ntfs_%04d_%s", outputDir, count, sanitizeFilename(entry.FileName))
		if err := os.WriteFile(outPath, data, 0o644); err != nil {
			if *verbose {
				fmt.Printf("[-] Failed to write %s: %v\n", outPath, err)
			}
			return
		}

		report.FilesFound++
		report.FilesByType[ext]++
		report.Entries = append(report.Entries, output.ReportEntry{
			FileName:   outPath,
			FileType:   ext,
			Size:       int64(len(data)),
			DiskOffset: entry.Offset,
			Source:     "ntfs_mft",
		})

		fmt.Printf("[✓] Recovered: %s → %s\n", entry.FileName, outPath)
	})

	if err != nil {
		fmt.Fprintf(os.Stderr, "Error scanning MFT: %v\n", err)
	}

	output.PrintSummary(report)
	if err := output.WriteReport(report, outputDir); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to write report: %v\n", err)
	}
}

// runExt4 recovers files from ext4 by scanning inode tables for deleted entries
// and parsing the filesystem journal for inode data that was zeroed on deletion.
func runExt4(args []string) {
	fs := flag.NewFlagSet("ext4", flag.ExitOnError)
	verbose := fs.Bool("v", false, "Verbose output")
	partOffset := fs.Int64("offset", 0, "Partition offset in bytes")
	maxGroups := fs.Int("max-groups", 100, "Maximum block groups to scan")
	typeFilters := fs.String("type", "", "File types to recover: pdf, zip, docx, xlsx, pptx")
	fs.Parse(args)

	if fs.NArg() < 2 {
		fmt.Fprintln(os.Stderr, "Usage: recover ext4 [options] <disk/image> <output_dir>")
		os.Exit(1)
	}

	diskPath := fs.Arg(0)
	outputDir := fs.Arg(1)
	targetTypes, err := parseTargetFileTypes(*typeFilters)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("[*] ext4 recovery on %s (offset: %d)\n", diskPath, *partOffset)
	if *typeFilters != "" {
		fmt.Printf("[*] File types: %s\n", *typeFilters)
	}

	reader, err := scanner.NewDiskReader(diskPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer reader.Close()

	parser, err := ext4.NewParser(reader, *partOffset)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("[*] Parsing ext4 superblock...")
	if err := parser.ParseSuperblock(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		fmt.Println("[!] ext4 not detected. Try 'recover carve' for raw recovery.")
		os.Exit(1)
	}

	sb := parser.SuperblockInfo()
	fmt.Printf("[*] Block size: %d bytes\n", sb.BlockSize)
	fmt.Printf("[*] Total inodes: %d\n", sb.TotalInodes)
	fmt.Printf("[*] Inodes per group: %d\n", sb.InodesPerGroup)

	// TRIM detection
	zeroPercent := reader.SampleForZeroBlocks(4096, 256)
	if zeroPercent > 50 {
		fmt.Printf("\n[!] WARNING: %.0f%% of sampled disk blocks are zeroed. This indicates SSD TRIM has run.\n", zeroPercent)
		fmt.Println("[!] Recovery chances are significantly reduced. Recently deleted files may be unrecoverable.")
	}

	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "Error creating output dir: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("[*] Scanning for deleted inodes (up to %d block groups)...\n", *maxGroups)

	report := &output.RecoveryReport{
		Timestamp:    time.Now(),
		Source:       diskPath,
		OutputDir:    outputDir,
		TotalScanned: reader.Size(),
		FilesByType:  make(map[string]int),
	}

	count := 0
	err = parser.ScanDeletedInodes(*maxGroups, func(inode *ext4.Inode) {
		if *verbose {
			fmt.Printf("[+] Found deleted inode %d (size: %d bytes)\n", inode.Number, inode.Size)
		}

		// Recover data
		data, err := parser.RecoverInodeData(inode)
		if err != nil {
			if *verbose {
				fmt.Printf("[-] Failed to recover inode %d: %v\n", inode.Number, err)
			}
			return
		}

		if len(data) == 0 {
			return
		}

		// Detect file type from content
		fileType, ext := detectFileType(data)
		if !isTargetFile(ext, targetTypes) {
			return
		}

		// Validate recovered file content before saving
		if !carver.ValidateRecoveredFile(data, ext, *verbose) {
			if *verbose {
				fmt.Printf("[-] Rejected inode %d: %s content validation failed\n", inode.Number, fileType)
			}
			return
		}

		count++
		outPath := fmt.Sprintf("%s/ext4_%04d_inode%d%s", outputDir, count, inode.Number, ext)
		if err := os.WriteFile(outPath, data, 0o644); err != nil {
			if *verbose {
				fmt.Printf("[-] Failed to write %s: %v\n", outPath, err)
			}
			return
		}

		report.FilesFound++
		report.FilesByType[fileType]++
		report.Entries = append(report.Entries, output.ReportEntry{
			FileName:   outPath,
			FileType:   fileType,
			Size:       int64(len(data)),
			DiskOffset: 0,
			Source:     "ext4_inode",
		})

		fmt.Printf("[✓] Recovered: inode %d → %s (%s, %d bytes)\n",
			inode.Number, outPath, fileType, len(data))
	})

	if err != nil {
		fmt.Fprintf(os.Stderr, "Error scanning inodes: %v\n", err)
	}

	// Attempt journal-based recovery for inodes with zeroed extents/blocks
	fmt.Println("[*] Scanning ext4 journal for recoverable inode data...")
	journalCount := 0
	err = parser.ScanJournalForInodes(func(inode *ext4.Inode) {
		if *verbose {
			fmt.Printf("[+] Found inode %d in journal (size: %d bytes)\n", inode.Number, inode.Size)
		}

		data, err := parser.RecoverInodeData(inode)
		if err != nil {
			if *verbose {
				fmt.Printf("[-] Failed to recover journal inode %d: %v\n", inode.Number, err)
			}
			return
		}

		if len(data) == 0 {
			return
		}

		fileType, ext := detectFileType(data)
		if !isTargetFile(ext, targetTypes) {
			return
		}

		if !carver.ValidateRecoveredFile(data, ext, *verbose) {
			if *verbose {
				fmt.Printf("[-] Rejected journal inode %d: %s content validation failed\n", inode.Number, fileType)
			}
			return
		}

		count++
		journalCount++
		outPath := fmt.Sprintf("%s/ext4_%04d_journal_inode%d%s", outputDir, count, inode.Number, ext)
		if err := os.WriteFile(outPath, data, 0o644); err != nil {
			if *verbose {
				fmt.Printf("[-] Failed to write %s: %v\n", outPath, err)
			}
			return
		}

		report.FilesFound++
		report.FilesByType[fileType]++
		report.Entries = append(report.Entries, output.ReportEntry{
			FileName:   outPath,
			FileType:   fileType,
			Size:       int64(len(data)),
			DiskOffset: 0,
			Source:     "ext4_journal",
		})

		fmt.Printf("[✓] Recovered from journal: inode %d → %s (%s, %d bytes)\n",
			inode.Number, outPath, fileType, len(data))
	})
	if err != nil {
		if *verbose {
			fmt.Printf("[-] Journal scan: %v\n", err)
		}
	}
	if journalCount > 0 {
		fmt.Printf("[*] Recovered %d additional files from journal\n", journalCount)
	}

	output.PrintSummary(report)
	if err := output.WriteReport(report, outputDir); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to write report: %v\n", err)
	}
}

func runAuto(args []string) {
	fs := flag.NewFlagSet("auto", flag.ExitOnError)
	verbose := fs.Bool("v", false, "Verbose output")
	partOffset := fs.Int64("offset", 0, "Partition offset in bytes")
	typeFilters := fs.String("type", "", "File types to recover: pdf, zip, docx, xlsx, pptx")
	fs.Parse(args)

	if fs.NArg() < 2 {
		fmt.Fprintln(os.Stderr, "Usage: recover auto [options] <disk/image> <output_dir>")
		os.Exit(1)
	}

	diskPath := fs.Arg(0)
	outputDir := fs.Arg(1)
	if _, err := parseTargetFileTypes(*typeFilters); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("[*] Auto-detecting filesystem on %s...\n", diskPath)
	if *typeFilters != "" {
		fmt.Printf("[*] File types: %s\n", *typeFilters)
	}

	reader, err := scanner.NewDiskReader(diskPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// Check for TRIM/zeroed blocks
	zeroPercent := reader.SampleForZeroBlocks(4096, 256)
	if zeroPercent > 50 {
		fmt.Printf("[!] WARNING: %.0f%% of sampled disk blocks are zeroed. This indicates SSD TRIM has run.\n", zeroPercent)
		fmt.Println("[!] Recovery chances are significantly reduced. Recently deleted files may be unrecoverable.")
	}

	// Try NTFS first
	ntfsParser, _ := ntfs.NewParser(reader, *partOffset)
	if err := ntfsParser.ParseBootSector(); err == nil {
		reader.Close()
		fmt.Println("[*] Detected NTFS filesystem")
		newArgs := []string{}
		if *verbose {
			newArgs = append(newArgs, "-v")
		}
		if *partOffset != 0 {
			newArgs = append(newArgs, "-offset", fmt.Sprintf("%d", *partOffset))
		}
		if *typeFilters != "" {
			newArgs = append(newArgs, "-type", *typeFilters)
		}
		newArgs = append(newArgs, diskPath, outputDir)
		runNTFS(newArgs)
		return
	}

	// Try ext4
	ext4Parser, _ := ext4.NewParser(reader, *partOffset)
	if err := ext4Parser.ParseSuperblock(); err == nil {
		reader.Close()
		fmt.Println("[*] Detected ext4 filesystem")
		newArgs := []string{}
		if *verbose {
			newArgs = append(newArgs, "-v")
		}
		if *partOffset != 0 {
			newArgs = append(newArgs, "-offset", fmt.Sprintf("%d", *partOffset))
		}
		if *typeFilters != "" {
			newArgs = append(newArgs, "-type", *typeFilters)
		}
		newArgs = append(newArgs, diskPath, outputDir)
		runExt4(newArgs)
		return
	}

	reader.Close()

	// Fallback to raw carving
	fmt.Println("[*] No known filesystem detected, falling back to raw carving")
	newArgs := []string{}
	if *verbose {
		newArgs = append(newArgs, "-v")
	}
	if *typeFilters != "" {
		newArgs = append(newArgs, "-type", *typeFilters)
	}
	newArgs = append(newArgs, diskPath, outputDir)
	runCarve(newArgs)
}

// ═══════════════════════════════════════════════════════════════════
// Interactive Mode
// ═══════════════════════════════════════════════════════════════════

// diskInfo represents a detected disk or partition.
type diskInfo struct {
	Path       string
	Size       string
	Model      string
	Type       string // "disk" or "part"
	MountPoint string
	FSType     string
}

func runInteractive() {
	fmt.Println()
	fmt.Println("╔══════════════════════════════════════════════════════════════╗")
	fmt.Println("║           recovery-soft — Interactive Recovery Mode          ║")
	fmt.Println("╠══════════════════════════════════════════════════════════════╣")
	fmt.Println("║  This wizard will guide you through:                         ║")
	fmt.Println("║    1. Selecting the source disk (where your data was)        ║")
	fmt.Println("║    2. Selecting the output location (where to save files)    ║")
	fmt.Println("║    3. Choosing the recovery method                           ║")
	fmt.Println("║                                                              ║")
	fmt.Println("║  Navigate: ↑/↓ arrows or j/k │ Select: Enter │ Quit: Ctrl+C  ║")
	fmt.Println("╚══════════════════════════════════════════════════════════════╝")

	// Check if running with elevated privileges
	if !isElevated() {
		tui.Warning("Not running as root/administrator. You may not be able to access raw disks.")
		tui.Info("Linux: sudo recover interactive  |  Windows: Run as Administrator")
		fmt.Println()
		cont, err := tui.Confirm("Continue anyway?", false)
		if err != nil || !cont {
			fmt.Println("  Exiting.")
			os.Exit(0)
		}
	}

	// Step 1: Select source disk
	tui.Header("STEP 1: Select SOURCE disk (recover FROM)")

	disks := listDisks()
	if len(disks) == 0 {
		fmt.Println("  No disks found. Make sure you're running with sudo.")
		os.Exit(1)
	}

	// Build menu items from disk list
	diskMenuItems := make([]tui.MenuItem, len(disks))
	for i, d := range disks {
		label := fmt.Sprintf("%-16s %s", d.Path, d.Size)
		desc := ""
		if d.MountPoint != "" {
			desc = fmt.Sprintf("[mounted: %s]", d.MountPoint)
		} else if d.Model != "" {
			desc = d.Model
		}
		if d.FSType != "" {
			desc = fmt.Sprintf("(%s) %s", d.FSType, desc)
		}
		diskMenuItems[i] = tui.MenuItem{
			Label:       label,
			Description: desc,
			Value:       d.Path,
		}
	}

	tui.Info("Use ↑/↓ to select the disk where your files WERE before formatting")
	fmt.Println()

	sourceIdx, err := tui.SelectOption("Select source disk:", diskMenuItems)
	if err != nil {
		fmt.Printf("\n  Cancelled.\n")
		os.Exit(0)
	}

	sourcePath := disks[sourceIdx].Path

	// Warn if mounted
	if disks[sourceIdx].MountPoint != "" {
		tui.Warning(fmt.Sprintf("%s is mounted at %s", sourcePath, disks[sourceIdx].MountPoint))
		tui.Info("For best results, unmount first or use a live USB.")
		cont, err := tui.Confirm("Continue with mounted disk?", false)
		if err != nil || !cont {
			fmt.Println("  Exiting.")
			os.Exit(0)
		}
	}

	tui.Success(fmt.Sprintf("Source: %s", sourcePath))

	// Step 2: Select output location
	tui.Header("STEP 2: Select OUTPUT location (save recovered files TO)")
	tui.Warning("Output MUST be on a DIFFERENT disk than the source!")
	fmt.Println()

	// Show mounted filesystems as options
	mounts := listMountedFilesystems()
	outputMenuItems := []tui.MenuItem{}
	for _, m := range mounts {
		// Skip the source disk's mount
		if m.MountPoint == disks[sourceIdx].MountPoint {
			continue
		}
		outputMenuItems = append(outputMenuItems, tui.MenuItem{
			Label:       m.MountPoint + "/recovered",
			Description: fmt.Sprintf("(%s free on %s)", m.Size, m.Device),
			Value:       m.MountPoint + "/recovered",
		})
	}
	outputMenuItems = append(outputMenuItems, tui.MenuItem{
		Label:       "./recovered",
		Description: "(current directory)",
		Value:       "./recovered",
	})
	outputMenuItems = append(outputMenuItems, tui.MenuItem{
		Label:       "Custom path...",
		Description: "(type your own path)",
		Value:       "__custom__",
	})

	outputIdx, err := tui.SelectOption("Select output destination:", outputMenuItems)
	if err != nil {
		fmt.Printf("\n  Cancelled.\n")
		os.Exit(0)
	}

	outputDir := outputMenuItems[outputIdx].Value
	if outputDir == "__custom__" {
		// Need raw mode off for text input — put terminal in raw mode inside TextInput
		oldState, _ := term.MakeRaw(int(os.Stdin.Fd()))
		customPath, err := tui.TextInput("Enter output directory path", "/mnt/external/recovered")
		term.Restore(int(os.Stdin.Fd()), oldState)
		if err != nil {
			fmt.Printf("\n  Cancelled.\n")
			os.Exit(0)
		}
		outputDir = customPath
	}

	// Expand ~ and make absolute
	if strings.HasPrefix(outputDir, "~/") {
		home, _ := os.UserHomeDir()
		outputDir = filepath.Join(home, outputDir[2:])
	}
	if !filepath.IsAbs(outputDir) {
		cwd, _ := os.Getwd()
		outputDir = filepath.Join(cwd, outputDir)
	}

	// Check same device
	if isOnSameDevice(sourcePath, outputDir) {
		tui.Warning("Output appears to be on the SAME disk as source!")
		tui.Info("This could OVERWRITE data you're trying to recover.")
		cont, err := tui.Confirm("Are you sure?", false)
		if err != nil || !cont {
			fmt.Println("  Exiting.")
			os.Exit(0)
		}
	}

	tui.Success(fmt.Sprintf("Output: %s", outputDir))

	// Step 3: Recovery method
	tui.Header("STEP 3: Select RECOVERY METHOD")

	methodItems := []tui.MenuItem{
		{Label: "Auto-detect", Description: "tries all methods automatically (recommended)", Value: "auto"},
		{Label: "NTFS recovery", Description: "best for formatted Windows drives — recovers filenames", Value: "ntfs"},
		{Label: "ext4 recovery", Description: "for recently deleted files on Linux", Value: "ext4"},
		{Label: "Raw carving", Description: "works on anything — no filenames recovered", Value: "carve"},
	}

	methodIdx, err := tui.SelectOption("Select recovery method:", methodItems)
	if err != nil {
		fmt.Printf("\n  Cancelled.\n")
		os.Exit(0)
	}

	method := methodItems[methodIdx].Value

	// Step 4: Target file types
	tui.Header("STEP 4: Select FILE TYPES")
	typeItems := []tui.MenuItem{
		{Label: "All supported files", Description: "PDF, ZIP, DOCX, XLSX, PPTX", Value: ""},
		{Label: "PDF only", Description: "%PDF documents", Value: "pdf"},
		{Label: "ZIP only", Description: "plain ZIP archives", Value: "zip"},
		{Label: "DOCX only", Description: "Word documents inside ZIP archives", Value: "docx"},
		{Label: "XLSX only", Description: "Excel workbooks inside ZIP archives", Value: "xlsx"},
		{Label: "PPTX only", Description: "PowerPoint files inside ZIP archives", Value: "pptx"},
	}
	typeIdx, err := tui.SelectOption("Select file types to recover:", typeItems)
	if err != nil {
		fmt.Printf("\n  Cancelled.\n")
		os.Exit(0)
	}
	typeFilter := typeItems[typeIdx].Value
	typeLabel := typeItems[typeIdx].Label

	// Step 5: Verbose?
	tui.Header("OPTIONS")
	verbose, err := tui.Confirm("Enable verbose output? (shows each file as it's found)", false)
	if err != nil {
		verbose = false
	}

	// Confirmation
	fmt.Println()
	fmt.Println("  ┌────────────────────────────────────────────────────────────────────────────────────────┐")
	fmt.Println("  │                                 RECOVERY SUMMARY                                       │")
	fmt.Println("  ├────────────────────────────────────────────────────────────────────────────────────────┤")
	fmt.Printf("   │  Source:   %-47s│\n", sourcePath)
	fmt.Printf("   │  Output:   %-47s│\n", outputDir)
	fmt.Printf("   │  Method:   %-47s│\n", methodItems[methodIdx].Label)
	fmt.Printf("   │  Types:    %-47s│\n", typeLabel)
	fmt.Printf("   │  Verbose:  %-47v│\n", verbose)
	fmt.Println("  └────────────────────────────────────────────────────────────────────────────────────────┘")
	fmt.Println()

	start, err := tui.Confirm("Start recovery?", true)
	if err != nil || !start {
		fmt.Println("  Cancelled.")
		os.Exit(0)
	}

	fmt.Println()
	fmt.Println("═══════════════════════════════════════════════════════════════")
	fmt.Println()

	// Check for TRIM/zeroed blocks before starting recovery
	trimReader, trimErr := scanner.NewDiskReader(sourcePath)
	if trimErr == nil {
		zeroPercent := trimReader.SampleForZeroBlocks(4096, 256)
		trimReader.Close()
		if zeroPercent > 50 {
			fmt.Printf("[!] WARNING: %.0f%% of sampled disk blocks are zeroed. This indicates SSD TRIM has run.\n", zeroPercent)
			fmt.Println("[!] Recovery chances are significantly reduced. Recently deleted files may be unrecoverable.")
			fmt.Println()
		}
	}

	// Build args and dispatch
	var args []string
	if verbose {
		args = append(args, "-v")
	}
	if typeFilter != "" {
		args = append(args, "-type", typeFilter)
	}
	args = append(args, sourcePath, outputDir)

	switch method {
	case "auto":
		runAuto(args)
	case "ntfs":
		runNTFS(args)
	case "ext4":
		runExt4(args)
	case "carve":
		runCarve(args)
	}
}

// ============================================================================
// Disk Enumeration (Cross-Platform)
// ============================================================================

// listDisks enumerates disks and partitions (cross-platform).
func listDisks() []diskInfo {
	// Try Windows first (wmic)
	disks := listDisksWindows()
	if len(disks) > 0 {
		return disks
	}

	// Try Linux (lsblk)
	disks = listDisksLinux()
	if len(disks) > 0 {
		return disks
	}

	// Fallback: /proc/partitions
	return listDisksFallback()
}

// listDisksWindows uses wmic and PowerShell to list disks on Windows.
func listDisksWindows() []diskInfo {
	// Try wmic for physical disks
	cmd := exec.Command("wmic", "diskdrive", "get", "DeviceID,Model,Size", "/format:csv")
	out, err := cmd.Output()
	if err != nil {
		return nil
	}

	var disks []diskInfo
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "Node") {
			continue
		}
		// CSV format: Node,DeviceID,Model,Size
		fields := strings.Split(line, ",")
		if len(fields) < 4 {
			continue
		}

		deviceID := strings.TrimSpace(fields[1])
		model := strings.TrimSpace(fields[2])
		sizeStr := strings.TrimSpace(fields[3])

		if deviceID == "" || deviceID == "DeviceID" {
			continue
		}

		size := sizeStr
		if sizeBytes, err := strconv.ParseInt(sizeStr, 10, 64); err == nil {
			size = formatSize(sizeBytes)
		}

		disks = append(disks, diskInfo{
			Path:  deviceID,
			Size:  size,
			Model: model,
			Type:  "disk",
		})
	}

	// Also list logical drives (partitions with drive letters)
	volCmd := exec.Command("wmic", "logicaldisk", "get", "DeviceID,Size,FileSystem,VolumeName", "/format:csv")
	volOut, err := volCmd.Output()
	if err == nil {
		for _, line := range strings.Split(string(volOut), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "Node") {
				continue
			}
			fields := strings.Split(line, ",")
			if len(fields) < 5 {
				continue
			}

			deviceID := strings.TrimSpace(fields[1])
			fsType := strings.TrimSpace(fields[2])
			sizeStr := strings.TrimSpace(fields[3])
			volumeName := strings.TrimSpace(fields[4])

			if deviceID == "" || deviceID == "DeviceID" {
				continue
			}

			size := sizeStr
			if sizeBytes, err := strconv.ParseInt(sizeStr, 10, 64); err == nil {
				size = formatSize(sizeBytes)
			}

			desc := volumeName
			if desc == "" {
				desc = ""
			}

			disks = append(disks, diskInfo{
				Path:       deviceID + "\\",
				Size:       size,
				Model:      desc,
				Type:       "part",
				FSType:     fsType,
				MountPoint: deviceID + "\\",
			})
		}
	}

	return disks
}

// listDisksLinux uses lsblk to enumerate disks on Linux.
func listDisksLinux() []diskInfo {
	cmd := exec.Command("lsblk", "-P", "-bno", "PATH,SIZE,TYPE,FSTYPE,MOUNTPOINT,MODEL")
	out, err := cmd.Output()
	if err != nil {
		return nil
	}

	var disks []diskInfo
	for _, line := range strings.Split(string(out), "\n") {
		fields := parseLSBLKPairs(line)
		if fields["PATH"] == "" || fields["SIZE"] == "" || fields["TYPE"] == "" {
			continue
		}
		if fields["TYPE"] == "loop" {
			continue
		}

		d := diskInfo{
			Path:       fields["PATH"],
			Type:       fields["TYPE"],
			FSType:     fields["FSTYPE"],
			MountPoint: fields["MOUNTPOINT"],
			Model:      fields["MODEL"],
		}

		if sizeBytes, err := strconv.ParseInt(fields["SIZE"], 10, 64); err == nil {
			d.Size = formatSize(sizeBytes)
		} else {
			d.Size = fields["SIZE"]
		}

		disks = append(disks, d)
	}

	return disks
}

func parseLSBLKPairs(line string) map[string]string {
	result := make(map[string]string)
	i := 0
	for i < len(line) {
		for i < len(line) && line[i] == ' ' {
			i++
		}
		keyStart := i
		for i < len(line) && line[i] != '=' {
			i++
		}
		if i >= len(line) {
			break
		}
		key := line[keyStart:i]
		i++
		if i >= len(line) || line[i] != '"' {
			break
		}
		i++
		var value strings.Builder
		for i < len(line) {
			if line[i] == '\\' && i+1 < len(line) {
				i++
				value.WriteByte(line[i])
				i++
				continue
			}
			if line[i] == '"' {
				i++
				break
			}
			value.WriteByte(line[i])
			i++
		}
		result[key] = value.String()
	}
	return result
}

// listDisksFallback reads /proc/partitions when lsblk isn't available.
func listDisksFallback() []diskInfo {
	data, err := os.ReadFile("/proc/partitions")
	if err != nil {
		return nil
	}

	var disks []diskInfo
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 4 {
			continue
		}
		if fields[0] == "major" {
			continue
		}
		name := fields[3]
		sizeKB, _ := strconv.ParseInt(fields[2], 10, 64)

		dtype := "disk"
		if len(name) > 0 && name[len(name)-1] >= '0' && name[len(name)-1] <= '9' {
			if strings.ContainsAny(name[:len(name)-1], "0123456789") || strings.HasPrefix(name, "nvme") {
				dtype = "part"
			}
		}

		disks = append(disks, diskInfo{
			Path: "/dev/" + name,
			Size: formatSize(sizeKB * 1024),
			Type: dtype,
		})
	}
	return disks
}

// mountInfo represents a mounted filesystem.
type mountInfo struct {
	Device     string
	MountPoint string
	Size       string
}

// listMountedFilesystems returns user-relevant mounted filesystems.
func listMountedFilesystems() []mountInfo {
	// Try Windows: list drive letters
	volCmd := exec.Command("wmic", "logicaldisk", "where", "DriveType=2 or DriveType=3", "get", "DeviceID,Size,VolumeName", "/format:csv")
	volOut, err := volCmd.Output()
	if err == nil {
		var mounts []mountInfo
		for _, line := range strings.Split(string(volOut), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "Node") {
				continue
			}
			fields := strings.Split(line, ",")
			if len(fields) < 4 {
				continue
			}
			deviceID := strings.TrimSpace(fields[1])
			sizeStr := strings.TrimSpace(fields[2])
			if deviceID == "" || deviceID == "DeviceID" {
				continue
			}
			// Skip C: drive (likely the source)
			if strings.ToUpper(deviceID) == "C:" {
				continue
			}
			size := sizeStr
			if sizeBytes, parseErr := strconv.ParseInt(sizeStr, 10, 64); parseErr == nil {
				size = formatSize(sizeBytes)
			}
			mounts = append(mounts, mountInfo{
				Device:     deviceID,
				MountPoint: deviceID + "\\",
				Size:       size,
			})
		}
		if len(mounts) > 0 {
			return mounts
		}
	}

	// Linux: use df
	cmd := exec.Command("df", "-h", "--output=source,target,size")
	out, err := cmd.Output()
	if err != nil {
		return nil
	}

	var mounts []mountInfo
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		if !strings.HasPrefix(fields[0], "/dev") {
			continue
		}
		mp := fields[1]
		if mp == "/" || strings.HasPrefix(mp, "/boot") || strings.HasPrefix(mp, "/snap") {
			continue
		}
		mounts = append(mounts, mountInfo{
			Device:     fields[0],
			MountPoint: mp,
			Size:       fields[2],
		})
	}
	return mounts
}

// resolveDeviceChoice turns user input into a device path.
func resolveDeviceChoice(input string, disks []diskInfo) string {
	// If it looks like a device path, use directly
	if strings.HasPrefix(input, "/dev/") || strings.HasPrefix(input, "\\\\.\\") {
		return input
	}

	// Windows drive letter (e.g., "D:" or "D:\")
	if len(input) >= 2 && input[1] == ':' {
		return input
	}

	// Try as a number
	num, err := strconv.Atoi(input)
	if err == nil && num >= 1 && num <= len(disks) {
		return disks[num-1].Path
	}

	return ""
}

// isOnSameDevice checks if outputDir resides on the same physical device as sourcePath.
func isOnSameDevice(sourcePath, outputDir string) bool {
	// Windows: compare PhysicalDrive number or drive letter mappings
	if strings.Contains(strings.ToUpper(sourcePath), "PHYSICALDRIVE") {
		// Can't easily determine which drive letters map to which physical drive
		// from pure Go without WMI queries. Be conservative — don't warn.
		return false
	}

	// Windows drive letters: if source is a drive letter and output is on same letter
	if len(sourcePath) >= 2 && sourcePath[1] == ':' && len(outputDir) >= 2 && outputDir[1] == ':' {
		return strings.ToUpper(sourcePath[:1]) == strings.ToUpper(outputDir[:1])
	}

	// Linux: strip partition number to get base device
	sourceBase := sourcePath
	if strings.Contains(sourceBase, "nvme") {
		if idx := strings.LastIndex(sourceBase, "p"); idx > 0 {
			sourceBase = sourceBase[:idx]
		}
	} else if strings.HasPrefix(sourceBase, "/dev/") {
		sourceBase = strings.TrimRight(sourceBase, "0123456789")
	}

	// Check what device the output directory is on using df
	cmd := exec.Command("df", "--output=source", outputDir)
	out, err := cmd.Output()
	if err != nil {
		parent := filepath.Dir(outputDir)
		cmd = exec.Command("df", "--output=source", parent)
		out, err = cmd.Output()
		if err != nil {
			return false
		}
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		return false
	}
	outputDevice := strings.TrimSpace(lines[len(lines)-1])

	return strings.HasPrefix(outputDevice, sourceBase)
}

// ============================================================================
// Utility Functions
// ============================================================================

// isElevated checks if the process has root/admin privileges.
func isElevated() bool {
	// Try Unix-style first
	if os.Geteuid() == 0 {
		return true
	}
	// On Windows, Geteuid() always returns -1, so check by trying to open a raw device
	// or check the "net session" trick
	cmd := exec.Command("net", "session")
	if err := cmd.Run(); err == nil {
		return true
	}
	return false
}

func parseTargetFileTypes(typeList string) (map[string]bool, error) {
	if strings.TrimSpace(typeList) == "" {
		return nil, nil
	}
	supported := map[string]bool{
		"pdf":  true,
		"zip":  true,
		"docx": true,
		"xlsx": true,
		"pptx": true,
	}
	selected := make(map[string]bool)
	for _, part := range strings.Split(typeList, ",") {
		key := strings.ToLower(strings.TrimSpace(part))
		if key == "" {
			continue
		}
		if !supported[key] {
			return nil, fmt.Errorf("unsupported file type %q (supported: pdf, zip, docx, xlsx, pptx)", key)
		}
		selected["."+key] = true
	}
	if len(selected) == 0 {
		return nil, fmt.Errorf("no file types selected")
	}
	return selected, nil
}

func isTargetFile(ext string, selected map[string]bool) bool {
	targets := map[string]bool{
		".pdf":  true,
		".zip":  true,
		".docx": true,
		".xlsx": true,
		".pptx": true,
	}
	if !targets[ext] {
		return false
	}
	if len(selected) == 0 {
		return true
	}
	return selected[ext]
}

func getExtension(filename string) string {
	for i := len(filename) - 1; i >= 0; i-- {
		if filename[i] == '.' {
			ext := ""
			for _, c := range filename[i:] {
				if c >= 'A' && c <= 'Z' {
					ext += string(rune(c + 32))
				} else {
					ext += string(c)
				}
			}
			return ext
		}
	}
	return ""
}

func detectFileType(data []byte) (string, string) {
	if len(data) < 8 {
		return "unknown", ""
	}

	// PDF
	if data[0] == 0x25 && data[1] == 0x50 && data[2] == 0x44 && data[3] == 0x46 {
		return "PDF", ".pdf"
	}

	// ZIP / Office documents
	if data[0] == 0x50 && data[1] == 0x4B && data[2] == 0x03 && data[3] == 0x04 {
		// Check for Office markers
		searchLen := 4096
		if len(data) < searchLen {
			searchLen = len(data)
		}
		content := string(data[:searchLen])
		if contains(content, "word/") || contains(content, "[Content_Types].xml") {
			return "DOCX", ".docx"
		}
		if contains(content, "xl/") {
			return "XLSX", ".xlsx"
		}
		if contains(content, "ppt/") {
			return "PPTX", ".pptx"
		}
		return "ZIP", ".zip"
	}

	return "unknown", ""
}

func contains(s, substr string) bool {
	if len(substr) > len(s) {
		return false
	}
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func sanitizeFilename(name string) string {
	result := make([]byte, 0, len(name))
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c == '/' || c == '\\' || c == ':' || c == '*' || c == '?' || c == '"' || c == '<' || c == '>' || c == '|' {
			result = append(result, '_')
		} else {
			result = append(result, c)
		}
	}
	return string(result)
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
