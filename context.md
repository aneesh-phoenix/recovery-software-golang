# Recovery-Soft Development Context

## Overview

`recovery-soft` is a file recovery tool written in Go (v0.1.0), focused on recovering **PDF** and **ZIP** (including DOCX/XLSX/PPTX) files from formatted or corrupted disks. It supports filesystem-aware recovery for **NTFS** and **ext4**, plus raw signature-based carving.

---

## Background

### User's Scenario
- A laptop running **Windows 11 Home** was formatted to install **Ubuntu 26**.
- Files in the Windows `Downloads` folder need to be recovered.
- The Ubuntu installation overwrote only a small portion of the disk (10-15 GB for base system).
- The bulk of old NTFS data likely still exists physically on disk.

### Key Factors
- **SSD with TRIM**: WD PC SN740 512GB SSD. TRIM can zero deleted blocks, reducing recovery chances.
- **Time sensitivity**: The sooner recovery is attempted, the better.
- **Best approach**: Boot from live USB, run NTFS mode first, then raw carving as fallback.

---

## Project Structure

```
recovery-soft/
├── cmd/recover/main.go        — CLI entry point (interactive + direct commands, ~1300 lines)
├── cmd/recover/main_test.go   — CLI integration test
├── pkg/
│   ├── signature/             — File type signatures (magic bytes for PDF, ZIP, DOCX, XLSX, PPTX)
│   │   ├── signature.go       — FileSignature type, ForTypes(), MatchHeader(), FindFooter()
│   │   └── signature_test.go
│   ├── scanner/               — Low-level disk I/O (DiskReader with mutex-protected ReadAt)
│   │   └── scanner.go         — NewDiskReader(), ReadAt(), ScanBlocks(), Size(), Close()
│   ├── carver/                — Signature-based file carving engine
│   │   ├── carver.go          — Scan loop, ZIP validation, Office XML classification, file saving
│   │   └── carver_test.go
│   ├── ntfs/                  — NTFS boot sector, MFT parsing, data run decoding
│   │   └── ntfs.go            — ParseBootSector(), ScanMFT(), ReadMFTEntry(), RecoverFileData()
│   ├── ext4/                  — ext4 superblock, group descriptors, inode parsing
│   │   └── ext4.go            — ParseSuperblock(), ScanDeletedInodes(), RecoverInodeData()
│   ├── tui/                   — Terminal UI with arrow-key navigation (ANSI escape codes)
│   │   └── tui.go             — SelectOption(), Confirm(), TextInput(), Header(), Success()
│   └── output/                — JSON report generation
│       └── output.go          — WriteReport(), PrintSummary()
├── docs/index.html            — Detailed HTML documentation (~110KB)
├── test_recovery.sh           — Integration test: creates NTFS image, adds files, reformats, recovers
├── go.mod / go.sum
├── README.md
└── .gitignore                 — Excludes *.exe and /recover binary
```

---

## Recovery Modes

1. **`interactive`** (alias `i`) — Arrow-key navigable TUI wizard (disk selection → output → method → file types → confirm)
2. **`carve`** — Raw file carving using signature detection (works on any filesystem or wiped disks)
3. **`ntfs`** — NTFS MFT recovery (recovers original filenames via data runs)
4. **`ext4`** — ext4 inode recovery (finds deleted inodes via deletion timestamps)
5. **`auto`** — Auto-detect: tries NTFS → ext4 → raw carving as fallback

---

## Supported File Types

| Type | Header | Footer/Validation |
|------|--------|-------------------|
| PDF  | `%PDF` (25 50 44 46) | `%%EOF` (25 25 45 4F 46) — last occurrence used |
| ZIP  | `PK\x03\x04` (50 4B 03 04) | End-of-central-directory (50 4B 05 06) + structural validation |
| DOCX | ZIP + contains `word/` | Same as ZIP |
| XLSX | ZIP + contains `xl/` | Same as ZIP |
| PPTX | ZIP + contains `ppt/` | Same as ZIP |

Office XML files are recovered as ZIPs first, then classified by their internal directory structure.

---

## CLI Flags

- `-v` — Verbose output
- `-type <types>` — Comma-separated file type filter (e.g., `pdf`, `docx`, `pdf,zip`)
- `-offset <bytes>` — Partition offset for filesystem-aware modes

---

## Interactive Mode (TUI)

- Arrow-key (↑/↓) and vim-key (j/k) navigation
- Enter to confirm, Ctrl+C/q to quit
- Automatic disk enumeration (Linux: `lsblk`, Windows: `wmic`)
- Mount detection and same-device warnings
- Built with raw ANSI escape sequences + `golang.org/x/term` (no external TUI framework)

---

## Cross-Platform Support

- **Linux**: Uses `lsblk`, `/proc/partitions`, `df` for disk enumeration. Raw access via `/dev/sdX`.
- **Windows**: Uses `wmic diskdrive` and `wmic logicaldisk`. Raw access via `\\.\PHYSICALDRIVE0`.
- Single codebase, cross-compiled with `GOOS=windows GOARCH=amd64`.

---

## Key Technical Decisions

| Decision | Rationale |
|----------|-----------|
| No external TUI framework | Raw ANSI + `x/term` keeps binary small, zero heavy dependencies |
| Sequential scanning (not parallel) | HDDs have single read head; SSD TRIM makes parallelism moot; simpler overlap buffer handling |
| 64-byte overlap between blocks | Catches file headers spanning block boundaries |
| PDF-aware footer finding (`findPDFEnd`) | Validates `%%EOF` is preceded by `startxref`; skips orphan `%%EOF` in random disk data |
| PDF structural validation | Checks for version header, object definitions, xref, trailer, and control-byte ratio in header region |
| ZIP local header field validation | Rejects false `PK\x03\x04` by checking version, compression method, and file name sanity |
| ZIP decompression test | Attempts to decompress at least one small file to catch structurally valid but data-corrupted archives |
| 100MB PDF read cap | Down from 500MB; prevents chasing `%%EOF` through hundreds of MB of unrelated disk data |
| Office XML classified post-recovery | Recover as ZIP first, then inspect internal paths + `[Content_Types].xml` to rename |
| Content validation on all recovery paths | NTFS and ext4 modes also validate before saving (via `ValidateRecoveredFile`) |
| ext4 journal scanning | Recovers inodes whose extents were zeroed by kernel on deletion; scans JBD2 journal for intact inode copies |
| TRIM/zero-block detection | Samples 256 blocks across disk; warns user if >50% are zeroed (SSD TRIM indicator) |
| Fragmented file warnings | When a file passes header check but fails full validation, warns about possible fragmentation instead of silent rejection |

---

## Dependencies

- `golang.org/x/term v0.44.0` — Raw terminal mode for interactive TUI
- `golang.org/x/sys v0.46.0` — Transitive dependency of x/term
- **No other external dependencies**

---

## Build & Test

```bash
# Build (Linux)
go build -o recover ./cmd/recover/

# Cross-compile (Windows)
GOOS=windows GOARCH=amd64 go build -o recover.exe ./cmd/recover/

# Unit tests
go test ./...

# Integration test (creates NTFS image, adds test files, reformats to ext4, runs recovery)
sudo ./test_recovery.sh
```

---

## Usage on Windows

### Disk Layout (User's Setup)
```
\\.\PHYSICALDRIVE0  — 476.94 GB  WD PC SN740 (laptop SSD, NTFS) — SOURCE
\\.\PHYSICALDRIVE1  — 14.45 GB   Sony USB pendrive — OUTPUT (D:\)
```

### How to Run
1. Open PowerShell as **Administrator**
2. Run: `D:\recover.exe interactive`
3. Select `\\.\PHYSICALDRIVE0` as source (not `C:\` — drive letters can't be read raw)
4. Select `D:\recovered` as output
5. Choose Auto-detect or NTFS method

**Important**: `\\.\PHYSICALDRIVE0` = raw disk access (sees deleted data). `C:\` = filesystem-level only (won't work for recovery).

---

## TRIM and Recovery

- TRIM tells the SSD to erase blocks no longer in use — data becomes **physically unrecoverable**.
- Ubuntu enables `fstrim.timer` by default (runs weekly).
- Windows runs TRIM via "Optimize Drives" (often automatic).
- HDDs don't have TRIM — much better recovery chances.
- Check TRIM on Linux: `systemctl status fstrim.timer`

---

## Documentation (docs/index.html)

Covers: What is File Recovery, Project Architecture, Scanner internals, File Signatures, Carving Engine, NTFS Recovery, ext4 Recovery, Usage Guide, Interactive Mode, Testing, Troubleshooting, Adding File Types (20+ signature reference), Adding Filesystems (FAT32 walkthrough).

---

## Future Enhancements (Not Implemented)

- Parallel scanning with goroutines (pipeline pattern)
- Additional file signatures (JPEG, PNG, MP4, etc.)
- FAT32/exFAT filesystem support
- Progress bar with ETA
- Resume interrupted scans
- Bifragment gap carving (attempt to reassemble 2-fragment files)
