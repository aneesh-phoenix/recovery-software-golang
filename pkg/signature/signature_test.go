package signature

import "testing"

func TestForTypesSelectsRequestedSignatures(t *testing.T) {
	sigs, err := ForTypes("pdf,zip,pdf")
	if err != nil {
		t.Fatalf("ForTypes returned error: %v", err)
	}
	if len(sigs) != 2 {
		t.Fatalf("expected 2 signatures, got %d", len(sigs))
	}
	if sigs[0].Name != "PDF" || sigs[1].Name != "ZIP" {
		t.Fatalf("unexpected signatures: %s, %s", sigs[0].Name, sigs[1].Name)
	}
}

func TestForTypesMapsOfficeTypesToZipSignature(t *testing.T) {
	sigs, err := ForTypes("docx,xlsx,pptx")
	if err != nil {
		t.Fatalf("ForTypes returned error: %v", err)
	}
	if len(sigs) != 1 {
		t.Fatalf("expected 1 raw signature, got %d", len(sigs))
	}
	if sigs[0].Name != "ZIP" {
		t.Fatalf("expected ZIP raw signature, got %s", sigs[0].Name)
	}
}

func TestForTypesRejectsUnsupportedTypes(t *testing.T) {
	if _, err := ForTypes("jpg"); err == nil {
		t.Fatal("expected unsupported jpg type to be rejected")
	}
}
