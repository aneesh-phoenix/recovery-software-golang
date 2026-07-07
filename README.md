# recovery-soft

A file recovery tool written in Go, focused on recovering **PDF** and **ZIP** (including DOCX/XLSX/PPTX) files from formatted or corrupted disks.

Features filesystem-aware recovery for **NTFS** and **ext4**.

## Build

```bash
go build -o recover ./cmd/recover/
```

## Usage

```bash
# Interactive mode (recommended — guides you with arrow-key navigation)
sudo ./recover interactive

# Auto-detect filesystem and recover
sudo ./recover auto /dev/sda ./recovered

# Raw file carving (works regardless of filesystem)
sudo ./recover carve /dev/sda ./recovered

# NTFS-aware recovery (recovers original filenames)
sudo ./recover ntfs /dev/sda2 ./recovered

# ext4-aware recovery (finds deleted inodes)
sudo ./recover ext4 /dev/sda2 ./recovered

# With verbose output
sudo ./recover carve -v /dev/sda ./recovered

# Specify partition offset
sudo ./recover ntfs -offset 1048576 /dev/sda ./recovered
```

## Recovery Modes

### `carve` — Raw File Carving
Scans raw disk bytes for known file signatures (headers/footers). Works on any filesystem or even fully wiped disks. Recovers files without original names.

### `ntfs` — NTFS MFT Recovery
Parses the NTFS Master File Table to find files with their original names, directory structure, and data run locations. Best for recovering from a disk that was previously NTFS.

### `ext4` — ext4 Inode Recovery
Scans ext4 inode tables for deleted entries. Uses deletion timestamps and extent/block pointer data to recover file contents.

### `auto` — Auto Detection
Tries NTFS, then ext4, then falls back to raw carving.

## Supported File Types

| Type | Detection Method |
|------|-----------------|
| PDF  | `%PDF` header + `%%EOF` footer |
| ZIP  | `PK\x03\x04` header + end-of-central-directory |
| DOCX | ZIP with `word/` content |
| XLSX | ZIP with `xl/` content |
| PPTX | ZIP with `ppt/` content |

## Testing

A test script simulates the Windows→Ubuntu formatting scenario:

```bash
sudo ./test_recovery.sh
```

This creates a 50MB NTFS image, adds PDF/ZIP files, reformats to ext4, then runs recovery.

## Architecture

```
cmd/recover/        - CLI entry point
pkg/
  signature/        - File type signatures and matching
  scanner/          - Low-level disk I/O
  carver/           - Signature-based file carving engine
  ntfs/             - NTFS boot sector, MFT, and data run parsing
  ext4/             - ext4 superblock, group descriptors, inode parsing
  output/           - Report generation
```

## Important Notes

- Always run with `sudo` for raw disk access
- **Never** write recovered files to the same disk you're recovering from
- The sooner you run recovery after data loss, the higher the success rate
- SSDs with TRIM enabled have lower recovery chances than HDDs
- For best results on a formatted Windows→Linux disk, try `ntfs` mode first, then `carve` as fallback
