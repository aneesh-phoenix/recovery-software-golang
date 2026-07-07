package main

import "testing"

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
