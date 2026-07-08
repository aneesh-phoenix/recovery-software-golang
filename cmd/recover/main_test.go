package main

import (
	"strings"
	"testing"
)

func TestParseLSBLKPairsPreservesEmptyFields(t *testing.T) {
	line := `PATH="/dev/nvme0n1" SIZE="1024000" TYPE="disk" FSTYPE="" MOUNTPOINT="" MODEL="SAMSUNG MZVL81T0HELB-00BH1"`

	fields := parseLSBLKPairs(line)
	if fields["MOUNTPOINT"] != "" {
		t.Fatalf("expected empty mountpoint, got %q", fields["MOUNTPOINT"])
	}
	if fields["MODEL"] != "SAMSUNG MZVL81T0HELB-00BH1" {
		t.Fatalf("unexpected model: %q", fields["MODEL"])
	}
}

func TestParseLSBLKPairsBasic(t *testing.T) {
	line := `PATH="/dev/sda" SIZE="500107862016" TYPE="disk" FSTYPE="ntfs" MOUNTPOINT="/mnt/data" MODEL="WDC WD5000"`

	fields := parseLSBLKPairs(line)
	if fields["PATH"] != "/dev/sda" {
		t.Errorf("PATH = %q, want /dev/sda", fields["PATH"])
	}
	if fields["SIZE"] != "500107862016" {
		t.Errorf("SIZE = %q, want 500107862016", fields["SIZE"])
	}
	if fields["TYPE"] != "disk" {
		t.Errorf("TYPE = %q, want disk", fields["TYPE"])
	}
	if fields["FSTYPE"] != "ntfs" {
		t.Errorf("FSTYPE = %q, want ntfs", fields["FSTYPE"])
	}
}

func TestParseLSBLKPairsWithSpacesInValue(t *testing.T) {
	line := `MODEL="Western Digital Elements" PATH="/dev/sdb"`
	fields := parseLSBLKPairs(line)
	if fields["MODEL"] != "Western Digital Elements" {
		t.Errorf("MODEL = %q, want 'Western Digital Elements'", fields["MODEL"])
	}
	if fields["PATH"] != "/dev/sdb" {
		t.Errorf("PATH = %q, want /dev/sdb", fields["PATH"])
	}
}

func TestParseLSBLKPairsEmptyInput(t *testing.T) {
	fields := parseLSBLKPairs("")
	if len(fields) != 0 {
		t.Errorf("expected empty map, got %v", fields)
	}
}

func TestParseLSBLKPairsSingleField(t *testing.T) {
	fields := parseLSBLKPairs(`NAME="sda"`)
	if fields["NAME"] != "sda" {
		t.Errorf("NAME = %q, want sda", fields["NAME"])
	}
}

func TestParseLSBLKPairsEscapedQuote(t *testing.T) {
	line := `LABEL="my\"disk" PATH="/dev/sda"`
	fields := parseLSBLKPairs(line)
	if fields["LABEL"] != `my"disk` {
		t.Errorf("LABEL = %q, want my\"disk", fields["LABEL"])
	}
}

// --- sanitizeFilename tests ---

func TestSanitizeFilename_Clean(t *testing.T) {
	if got := sanitizeFilename("report.pdf"); got != "report.pdf" {
		t.Errorf("got %q, want report.pdf", got)
	}
}

func TestSanitizeFilename_ReplaceSlashes(t *testing.T) {
	if got := sanitizeFilename("path/to\\file.pdf"); got != "path_to_file.pdf" {
		t.Errorf("got %q, want path_to_file.pdf", got)
	}
}

func TestSanitizeFilename_ReplaceAllSpecialChars(t *testing.T) {
	input := `doc:file*name?.pdf<>"pipe|`
	got := sanitizeFilename(input)
	if strings.ContainsAny(got, `/\:*?"<>|`) {
		t.Errorf("sanitized name still contains special chars: %q", got)
	}
}

func TestSanitizeFilename_EmptyString(t *testing.T) {
	if got := sanitizeFilename(""); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestSanitizeFilename_AllSpecial(t *testing.T) {
	got := sanitizeFilename(`/\:*?"<>|`)
	for _, c := range got {
		if c != '_' {
			t.Errorf("expected all underscores, got %q", got)
			break
		}
	}
}

// --- contains tests ---

func TestContains_Found(t *testing.T) {
	if !contains("hello world", "world") {
		t.Error("expected contains to find 'world'")
	}
}

func TestContains_NotFound(t *testing.T) {
	if contains("hello world", "xyz") {
		t.Error("expected contains to NOT find 'xyz'")
	}
}

func TestContains_EmptySubstr(t *testing.T) {
	if !contains("hello", "") {
		t.Error("expected empty substr to be found")
	}
}

func TestContains_SubstrLongerThanString(t *testing.T) {
	if contains("hi", "hello world") {
		t.Error("expected longer substr to NOT be found")
	}
}

func TestContains_ExactMatch(t *testing.T) {
	if !contains("exact", "exact") {
		t.Error("expected exact match to be found")
	}
}

func TestContains_AtStart(t *testing.T) {
	if !contains("prefix_suffix", "prefix") {
		t.Error("expected prefix to be found")
	}
}

func TestContains_AtEnd(t *testing.T) {
	if !contains("prefix_suffix", "suffix") {
		t.Error("expected suffix to be found")
	}
}

// --- detectFileType tests ---

func TestDetectFileType_PDF(t *testing.T) {
	data := append([]byte{0x25, 0x50, 0x44, 0x46}, make([]byte, 10)...)
	ft, ext := detectFileType(data)
	if ft != "PDF" || ext != ".pdf" {
		t.Errorf("got (%q, %q), want (PDF, .pdf)", ft, ext)
	}
}

func TestDetectFileType_ZIP(t *testing.T) {
	data := append([]byte{0x50, 0x4B, 0x03, 0x04}, make([]byte, 100)...)
	ft, ext := detectFileType(data)
	if ft != "ZIP" || ext != ".zip" {
		t.Errorf("got (%q, %q), want (ZIP, .zip)", ft, ext)
	}
}

func TestDetectFileType_DOCX(t *testing.T) {
	data := append([]byte{0x50, 0x4B, 0x03, 0x04}, []byte("some stuff word/ more stuff")...)
	ft, ext := detectFileType(data)
	if ft != "DOCX" || ext != ".docx" {
		t.Errorf("got (%q, %q), want (DOCX, .docx)", ft, ext)
	}
}

func TestDetectFileType_XLSX(t *testing.T) {
	data := append([]byte{0x50, 0x4B, 0x03, 0x04}, []byte("some stuff xl/ more stuff")...)
	ft, ext := detectFileType(data)
	if ft != "XLSX" || ext != ".xlsx" {
		t.Errorf("got (%q, %q), want (XLSX, .xlsx)", ft, ext)
	}
}

func TestDetectFileType_PPTX(t *testing.T) {
	data := append([]byte{0x50, 0x4B, 0x03, 0x04}, []byte("some stuff ppt/ more stuff")...)
	ft, ext := detectFileType(data)
	if ft != "PPTX" || ext != ".pptx" {
		t.Errorf("got (%q, %q), want (PPTX, .pptx)", ft, ext)
	}
}

func TestDetectFileType_DOCXWithContentTypes(t *testing.T) {
	data := append([]byte{0x50, 0x4B, 0x03, 0x04}, []byte("junk [Content_Types].xml junk")...)
	ft, ext := detectFileType(data)
	if ft != "DOCX" || ext != ".docx" {
		t.Errorf("got (%q, %q), want (DOCX, .docx)", ft, ext)
	}
}

func TestDetectFileType_Unknown(t *testing.T) {
	data := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 0x4A, 0x46} // JPEG header
	ft, ext := detectFileType(data)
	if ft != "unknown" || ext != "" {
		t.Errorf("got (%q, %q), want (unknown, '')", ft, ext)
	}
}

func TestDetectFileType_TooShort(t *testing.T) {
	ft, ext := detectFileType([]byte{0x25, 0x50})
	if ft != "unknown" || ext != "" {
		t.Errorf("got (%q, %q), want (unknown, '')", ft, ext)
	}
}

func TestDetectFileType_Empty(t *testing.T) {
	ft, ext := detectFileType(nil)
	if ft != "unknown" || ext != "" {
		t.Errorf("got (%q, %q), want (unknown, '')", ft, ext)
	}
}

// --- getExtension tests ---

func TestGetExtension_Normal(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"report.pdf", ".pdf"},
		{"file.DOCX", ".docx"},
		{"archive.ZIP", ".zip"},
		{"data.xlsx", ".xlsx"},
		{"slides.PPTX", ".pptx"},
		{"no_extension", ""},
		{"multiple.dots.txt", ".txt"},
		{".hidden", ".hidden"},
		{"", ""},
		{"UPPER.PDF", ".pdf"},
		{"MiXeD.PdF", ".pdf"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := getExtension(tt.input)
			if got != tt.want {
				t.Errorf("getExtension(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// --- isTargetFile tests ---

func TestIsTargetFile_NoFilter(t *testing.T) {
	// When selected is nil/empty, all supported types are targets
	if !isTargetFile(".pdf", nil) {
		t.Error("expected .pdf to be target with nil filter")
	}
	if !isTargetFile(".docx", nil) {
		t.Error("expected .docx to be target with nil filter")
	}
	if isTargetFile(".txt", nil) {
		t.Error("expected .txt to NOT be target (not a supported type)")
	}
	if isTargetFile("", nil) {
		t.Error("expected empty ext to NOT be target")
	}
}

func TestIsTargetFile_WithFilter(t *testing.T) {
	filter := map[string]bool{".pdf": true}

	if !isTargetFile(".pdf", filter) {
		t.Error("expected .pdf to be target with pdf filter")
	}
	if isTargetFile(".docx", filter) {
		t.Error("expected .docx to NOT be target with pdf-only filter")
	}
	if isTargetFile(".zip", filter) {
		t.Error("expected .zip to NOT be target with pdf-only filter")
	}
}

func TestIsTargetFile_UnsupportedType(t *testing.T) {
	if isTargetFile(".jpg", nil) {
		t.Error("expected .jpg to NOT be target (unsupported)")
	}
	if isTargetFile(".exe", map[string]bool{".exe": true}) {
		t.Error("expected .exe to NOT be target even if in filter (unsupported base type)")
	}
}

// --- parseTargetFileTypes tests ---

func TestParseTargetFileTypes_Empty(t *testing.T) {
	result, err := parseTargetFileTypes("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil {
		t.Errorf("expected nil for empty input, got %v", result)
	}
}

func TestParseTargetFileTypes_SingleType(t *testing.T) {
	result, err := parseTargetFileTypes("pdf")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result[".pdf"] {
		t.Errorf("expected .pdf in result, got %v", result)
	}
	if len(result) != 1 {
		t.Errorf("expected 1 entry, got %d", len(result))
	}
}

func TestParseTargetFileTypes_MultipleTypes(t *testing.T) {
	result, err := parseTargetFileTypes("pdf,docx,xlsx")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result[".pdf"] || !result[".docx"] || !result[".xlsx"] {
		t.Errorf("missing expected types in %v", result)
	}
	if len(result) != 3 {
		t.Errorf("expected 3 entries, got %d", len(result))
	}
}

func TestParseTargetFileTypes_CaseInsensitive(t *testing.T) {
	result, err := parseTargetFileTypes("PDF,DOCX")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result[".pdf"] || !result[".docx"] {
		t.Errorf("expected case-insensitive matching, got %v", result)
	}
}

func TestParseTargetFileTypes_WithSpaces(t *testing.T) {
	result, err := parseTargetFileTypes(" pdf , zip ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result[".pdf"] || !result[".zip"] {
		t.Errorf("expected space-trimmed types, got %v", result)
	}
}

func TestParseTargetFileTypes_Unsupported(t *testing.T) {
	_, err := parseTargetFileTypes("jpg")
	if err == nil {
		t.Fatal("expected error for unsupported type")
	}
	if !strings.Contains(err.Error(), "unsupported") {
		t.Errorf("expected 'unsupported' in error, got: %v", err)
	}
}

func TestParseTargetFileTypes_MixedValidInvalid(t *testing.T) {
	_, err := parseTargetFileTypes("pdf,mp3")
	if err == nil {
		t.Fatal("expected error for unsupported type in mix")
	}
}

func TestParseTargetFileTypes_AllSupported(t *testing.T) {
	result, err := parseTargetFileTypes("pdf,zip,docx,xlsx,pptx")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 5 {
		t.Errorf("expected 5 entries, got %d", len(result))
	}
}

func TestParseTargetFileTypes_WhitespaceOnly(t *testing.T) {
	result, err := parseTargetFileTypes("   ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil {
		t.Errorf("expected nil for whitespace-only input, got %v", result)
	}
}

// --- formatSize tests ---

func TestFormatSize_Bytes(t *testing.T) {
	tests := []struct {
		input int64
		want  string
	}{
		{0, "0 bytes"},
		{1, "1 bytes"},
		{512, "512 bytes"},
		{1023, "1023 bytes"},
	}
	for _, tt := range tests {
		got := formatSize(tt.input)
		if got != tt.want {
			t.Errorf("formatSize(%d) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestFormatSize_KB(t *testing.T) {
	got := formatSize(1024)
	if got != "1.00 KB" {
		t.Errorf("formatSize(1024) = %q, want '1.00 KB'", got)
	}
	got = formatSize(5 * 1024)
	if got != "5.00 KB" {
		t.Errorf("formatSize(5120) = %q, want '5.00 KB'", got)
	}
}

func TestFormatSize_MB(t *testing.T) {
	got := formatSize(1024 * 1024)
	if got != "1.00 MB" {
		t.Errorf("formatSize(1MB) = %q, want '1.00 MB'", got)
	}
	got = formatSize(50 * 1024 * 1024)
	if got != "50.00 MB" {
		t.Errorf("formatSize(50MB) = %q, want '50.00 MB'", got)
	}
}

func TestFormatSize_GB(t *testing.T) {
	got := formatSize(1024 * 1024 * 1024)
	if got != "1.00 GB" {
		t.Errorf("formatSize(1GB) = %q, want '1.00 GB'", got)
	}
	got = formatSize(500 * 1024 * 1024 * 1024)
	if got != "500.00 GB" {
		t.Errorf("formatSize(500GB) = %q, want '500.00 GB'", got)
	}
}
