package carver

import (
	"archive/zip"
	"bytes"
	"testing"

	"github.com/aneesh/recovery-soft/pkg/signature"
)

func TestFindValidZipEndIgnoresTrailingData(t *testing.T) {
	archive := makeZip(t, map[string]string{
		"hello.txt": "hello",
	})
	data := append(append([]byte{}, archive...), []byte("trailing disk bytes")...)

	end, ok := findValidZipEnd(data, 0)
	if !ok {
		t.Fatal("expected valid ZIP end")
	}
	if end != len(archive) {
		t.Fatalf("expected archive end %d, got %d", len(archive), end)
	}

	if ok, err := validateZip(data); ok || err == nil {
		t.Fatal("expected archive with trailing data to be rejected")
	}
}

func TestValidateZipRejectsInnerLocalHeader(t *testing.T) {
	archive := makeZip(t, map[string]string{
		"first.txt":  "first",
		"second.txt": "second",
	})
	innerHeader := bytes.Index(archive[4:], []byte{0x50, 0x4b, 0x03, 0x04})
	if innerHeader < 0 {
		t.Fatal("test archive did not contain a second local header")
	}
	innerHeader += 4

	if ok, err := validateZip(archive[innerHeader:]); ok || err == nil {
		t.Fatal("expected ZIP candidate from an inner local header to be rejected")
	}
}

func TestClassifyZipUsesDirectoryNames(t *testing.T) {
	archive := makeZip(t, map[string]string{
		"[Content_Types].xml": "",
		"word/document.xml":   "<w:document/>",
	})
	fallback := &signature.FileSignature{
		Name:      "ZIP",
		Extension: ".zip",
		Header:    []byte{0x50, 0x4b, 0x03, 0x04},
		Footer:    []byte{0x50, 0x4b, 0x05, 0x06},
		MaxSize:   1024 * 1024,
	}

	actual := (&Carver{}).classifyZip(archive, fallback)
	if actual.Name != "DOCX" || actual.Extension != ".docx" {
		t.Fatalf("expected DOCX classification, got %s %s", actual.Name, actual.Extension)
	}
}

func TestCarverOutputFilterCanSelectOfficeTypes(t *testing.T) {
	filter, err := parseOutputTypeFilter("docx")
	if err != nil {
		t.Fatalf("parseOutputTypeFilter returned error: %v", err)
	}
	c := &Carver{outputTypes: filter}

	if !c.shouldSave(&signature.FileSignature{Extension: ".docx"}) {
		t.Fatal("expected DOCX to be saved")
	}
	if c.shouldSave(&signature.FileSignature{Extension: ".zip"}) {
		t.Fatal("did not expect plain ZIP to be saved by DOCX-only filter")
	}
}

func TestFindFooterEndHonorsSearchWindowOverlap(t *testing.T) {
	data := []byte("%PDF body %%EOF trailing")
	footer := []byte("%%EOF")

	end := findFooterEnd(data, footer, len("%PDF body %%")-2)
	if end != len("%PDF body %%EOF") {
		t.Fatalf("expected footer end %d, got %d", len("%PDF body %%EOF"), end)
	}

	if got := findFooterEnd(data, footer, len("%PDF body %%EOF")); got != -1 {
		t.Fatalf("expected no match after footer, got %d", got)
	}
}

func makeZip(t *testing.T, files map[string]string) []byte {
	t.Helper()

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("Create(%q): %v", name, err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatalf("Write(%q): %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return buf.Bytes()
}
