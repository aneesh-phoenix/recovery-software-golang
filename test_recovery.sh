#!/bin/bash
# Test script: creates a disk image, writes files to it, reformats, then recovers.
# This simulates the Windows→Ubuntu scenario.

set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
IMG="$SCRIPT_DIR/test_disk.img"
MNT="$SCRIPT_DIR/test_mnt"
OUT="$SCRIPT_DIR/test_output"
BINARY="$SCRIPT_DIR/recover"

cleanup() {
    sudo umount "$MNT" 2>/dev/null || true
    rm -rf "$MNT" "$OUT" "$IMG"
}

echo "=== Recovery Tool Test Script ==="
echo ""

# Clean previous runs
cleanup
mkdir -p "$MNT" "$OUT"

# Step 1: Create a 50MB disk image with NTFS
echo "[1/5] Creating 50MB NTFS disk image..."
dd if=/dev/zero of="$IMG" bs=1M count=50 2>/dev/null
mkfs.ntfs -F -Q "$IMG" >/dev/null 2>&1

# Step 2: Mount and add test files
echo "[2/5] Adding test files to NTFS image..."
sudo mount -o loop "$IMG" "$MNT"

# Create a PDF file
echo "%PDF-1.4
1 0 obj
<< /Type /Catalog /Pages 2 0 R >>
endobj
2 0 obj
<< /Type /Pages /Kids [3 0 R] /Count 1 >>
endobj
3 0 obj
<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R >>
endobj
4 0 obj
<< /Length 44 >>
stream
BT /F1 12 Tf 100 700 Td (Hello Recovery!) Tj ET
endstream
endobj
xref
0 5
0000000000 65535 f 
0000000009 00000 n 
0000000058 00000 n 
0000000115 00000 n 
0000000214 00000 n 
trailer << /Size 5 /Root 1 0 R >>
startxref
312
%%EOF" | sudo tee "$MNT/test_document.pdf" >/dev/null

# Create a ZIP file
echo "test zip content" | sudo tee "$MNT/temp.txt" >/dev/null
cd "$MNT" && sudo zip -q test_archive.zip temp.txt 2>/dev/null && cd "$SCRIPT_DIR"
sudo rm -f "$MNT/temp.txt"

# Create another PDF
echo "%PDF-1.5
1 0 obj<</Type/Catalog/Pages 2 0 R>>endobj
2 0 obj<</Type/Pages/Kids[3 0 R]/Count 1>>endobj
3 0 obj<</Type/Page/Parent 2 0 R/MediaBox[0 0 595 842]>>endobj
xref
0 4
trailer<</Size 4/Root 1 0 R>>
startxref
0
%%EOF" | sudo tee "$MNT/important_report.pdf" >/dev/null

sudo umount "$MNT"

# Step 3: "Format" to ext4 (simulating Ubuntu install)
echo "[3/5] Reformatting to ext4 (simulating Ubuntu install)..."
mkfs.ext4 -F -q "$IMG" 2>/dev/null

# Step 4: Run recovery
echo "[4/5] Running recovery tool..."
echo ""
"$BINARY" carve -v "$IMG" "$OUT"

# Step 5: Verify results
echo ""
echo "[5/5] Checking recovered files..."
echo ""
if ls "$OUT"/*.pdf 2>/dev/null | head -1 >/dev/null; then
    echo "✓ PDF files recovered!"
    for f in "$OUT"/*.pdf; do
        echo "  - $f ($(wc -c < "$f") bytes)"
    done
else
    echo "✗ No PDF files recovered"
fi

if ls "$OUT"/*.zip 2>/dev/null | head -1 >/dev/null; then
    echo "✓ ZIP files recovered!"
    for f in "$OUT"/*.zip; do
        echo "  - $f ($(wc -c < "$f") bytes)"
    done
else
    echo "✗ No ZIP files recovered"
fi

echo ""
echo "Recovery report: $OUT/recovery_report.json"
echo ""

# Cleanup
rm -rf "$MNT"
echo "Done! Check $OUT for recovered files."
echo "(Run 'rm -rf test_disk.img test_output' to clean up)"
