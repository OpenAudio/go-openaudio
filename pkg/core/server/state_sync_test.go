package server

import "testing"

func TestSnapshotDirHasMinFreeBytes(t *testing.T) {
	dir := t.TempDir()

	freeBytes, err := snapshotDirFreeBytes(dir)
	if err != nil {
		t.Fatalf("snapshotDirFreeBytes() error = %v", err)
	}
	if freeBytes <= 0 {
		t.Fatalf("snapshotDirFreeBytes() = %d, want positive free bytes", freeBytes)
	}

	gotFree, ok, err := snapshotDirHasMinFreeBytes(dir, 0)
	if err != nil {
		t.Fatalf("snapshotDirHasMinFreeBytes(0) error = %v", err)
	}
	if !ok {
		t.Fatal("snapshotDirHasMinFreeBytes(0) = false, want true")
	}
	if gotFree != 0 {
		t.Fatalf("snapshotDirHasMinFreeBytes(0) free = %d, want 0 because guard is disabled", gotFree)
	}

	gotFree, ok, err = snapshotDirHasMinFreeBytes(dir, freeBytes)
	if err != nil {
		t.Fatalf("snapshotDirHasMinFreeBytes(freeBytes) error = %v", err)
	}
	if !ok {
		t.Fatal("snapshotDirHasMinFreeBytes(freeBytes) = false, want true")
	}
	if gotFree != freeBytes {
		t.Fatalf("snapshotDirHasMinFreeBytes(freeBytes) free = %d, want %d", gotFree, freeBytes)
	}

	_, ok, err = snapshotDirHasMinFreeBytes(dir, freeBytes+1)
	if err != nil {
		t.Fatalf("snapshotDirHasMinFreeBytes(freeBytes+1) error = %v", err)
	}
	if ok {
		t.Fatal("snapshotDirHasMinFreeBytes(freeBytes+1) = true, want false")
	}
}
