// Package signature defines file type signatures used for carving.
package signature

import (
	"fmt"
	"strings"
)

// FileSignature represents a known file type with its magic bytes.
type FileSignature struct {
	Name      string
	Extension string
	Header    []byte
	Footer    []byte
	MaxSize   int64 // Maximum expected file size in bytes
}

// Registry holds all known raw file signatures.
// Office Open XML documents are ZIP archives, so they are classified after ZIP
// recovery instead of being scanned as separate signatures.
var Registry = []FileSignature{
	{
		Name:      "PDF",
		Extension: ".pdf",
		Header:    []byte{0x25, 0x50, 0x44, 0x46},       // %PDF
		Footer:    []byte{0x25, 0x25, 0x45, 0x4F, 0x46}, // %%EOF
		MaxSize:   500 * 1024 * 1024,                    // 500MB
	},
	{
		Name:      "ZIP",
		Extension: ".zip",
		Header:    []byte{0x50, 0x4B, 0x03, 0x04}, // PK\x03\x04
		Footer:    []byte{0x50, 0x4B, 0x05, 0x06}, // End of central directory
		MaxSize:   2 * 1024 * 1024 * 1024,         // 2GB
	},
}

// ForTypes returns raw signatures needed for a comma-separated output type list.
// Empty input selects all raw signatures. Office document types use the ZIP
// raw signature and are classified after recovery.
func ForTypes(typeList string) ([]FileSignature, error) {
	if strings.TrimSpace(typeList) == "" {
		return append([]FileSignature{}, Registry...), nil
	}

	byName := make(map[string]FileSignature, len(Registry))
	var zipSig FileSignature
	for _, sig := range Registry {
		byName[strings.ToLower(sig.Name)] = sig
		byName[strings.TrimPrefix(strings.ToLower(sig.Extension), ".")] = sig
		if sig.Extension == ".zip" {
			zipSig = sig
		}
	}
	byName["docx"] = zipSig
	byName["xlsx"] = zipSig
	byName["pptx"] = zipSig

	var selected []FileSignature
	seen := make(map[string]bool)
	for _, part := range strings.Split(typeList, ",") {
		key := strings.ToLower(strings.TrimSpace(part))
		if key == "" {
			continue
		}
		sig, ok := byName[key]
		if !ok {
			return nil, fmt.Errorf("unsupported file type %q (supported: pdf, zip, docx, xlsx, pptx)", key)
		}
		if seen[sig.Name] {
			continue
		}
		selected = append(selected, sig)
		seen[sig.Name] = true
	}

	if len(selected) == 0 {
		return nil, fmt.Errorf("no file types selected")
	}
	return selected, nil
}

// MatchHeader checks if the given data starts with the signature header.
func (s *FileSignature) MatchHeader(data []byte) bool {
	if len(data) < len(s.Header) {
		return false
	}
	for i, b := range s.Header {
		if data[i] != b {
			return false
		}
	}
	return true
}

// FindFooter searches for the footer in data and returns its position (end of footer).
// Returns -1 if not found.
func (s *FileSignature) FindFooter(data []byte) int64 {
	if len(s.Footer) == 0 {
		return -1
	}
	footerLen := len(s.Footer)
	for i := len(data) - footerLen; i >= 0; i-- {
		match := true
		for j := 0; j < footerLen; j++ {
			if data[i+j] != s.Footer[j] {
				match = false
				break
			}
		}
		if match {
			return int64(i + footerLen)
		}
	}
	return -1
}
