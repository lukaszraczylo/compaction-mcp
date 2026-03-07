package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPersistSaveLoad(t *testing.T) {
	dir := t.TempDir()
	store1 := NewStore(10000)

	if _, err := store1.Add("first item content", "first summary", []string{"tag1"}, 5); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, err := store1.Add("second item content", "", []string{"tag2", "tag3"}, 8); err != nil {
		t.Fatalf("Add: %v", err)
	}

	p1, err := NewPersister(dir, store1)
	if err != nil {
		t.Fatalf("NewPersister: %v", err)
	}

	errSave := p1.Save()
	if errSave != nil {
		t.Fatalf("Save: %v", errSave)
	}

	// Load into a fresh store and verify roundtrip.
	store2 := NewStore(10000)
	p2, errP2 := NewPersister(dir, store2)
	if errP2 != nil {
		t.Fatalf("NewPersister (store2): %v", errP2)
	}

	errLoad := p2.Load()
	if errLoad != nil {
		t.Fatalf("Load: %v", errLoad)
	}

	_, _, count1, _ := store1.Status()
	_, _, count2, _ := store2.Status()

	if count2 != count1 {
		t.Fatalf("item count mismatch: got %d, want %d", count2, count1)
	}

	// Verify individual items survived the roundtrip.
	items := store2.Query("", nil, 100)
	if len(items) != 2 {
		t.Fatalf("expected 2 items from query, got %d", len(items))
	}

	found := make(map[string]bool)
	for _, item := range items {
		found[item.Content] = true
		if item.Tokens <= 0 {
			t.Errorf("item %s has non-positive token count: %d", item.ID, item.Tokens)
		}
	}

	if !found["first item content"] {
		t.Error("missing 'first item content' after load")
	}
	if !found["second item content"] {
		t.Error("missing 'second item content' after load")
	}
}

func TestPersistAtomicWrite(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(10000)
	if _, err := store.Add("test content", "", nil, 5); err != nil {
		t.Fatalf("Add: %v", err)
	}

	p, err := NewPersister(dir, store)
	if err != nil {
		t.Fatalf("NewPersister: %v", err)
	}

	errSave := p.Save()
	if errSave != nil {
		t.Fatalf("Save: %v", errSave)
	}

	// state.json should exist.
	_, errStat := os.Stat(filepath.Join(dir, "state.json"))
	if errStat != nil {
		t.Fatalf("state.json missing: %v", errStat)
	}

	// Temp file should not linger.
	_, errTmp := os.Stat(filepath.Join(dir, "state.json.tmp"))
	if !os.IsNotExist(errTmp) {
		t.Fatal("state.json.tmp should not exist after successful save")
	}
}

func TestPersistLoadMissing(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(10000)

	p, err := NewPersister(dir, store)
	if err != nil {
		t.Fatalf("NewPersister: %v", err)
	}

	// Loading from a directory with no state.json should succeed (fresh start).
	errLoad := p.Load()
	if errLoad != nil {
		t.Fatalf("Load on missing file should return nil, got: %v", errLoad)
	}

	_, _, count, _ := store.Status()
	if count != 0 {
		t.Fatalf("expected 0 items after loading missing file, got %d", count)
	}
}

func TestPersistLoadCorrupted(t *testing.T) {
	dir := t.TempDir()

	// Write garbage to state.json.
	errWrite := os.WriteFile(filepath.Join(dir, "state.json"), []byte("{{{garbage"), 0o600)
	if errWrite != nil {
		t.Fatalf("writing corrupt file: %v", errWrite)
	}

	store := NewStore(10000)
	p, err := NewPersister(dir, store)
	if err != nil {
		t.Fatalf("NewPersister: %v", err)
	}

	if p.Load() == nil {
		t.Fatal("Load should return error for corrupted file")
	}
}

func TestPersistMarkDirty(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(10000)

	p, err := NewPersister(dir, store)
	if err != nil {
		t.Fatalf("NewPersister: %v", err)
	}

	// Initially not dirty -- Start + Stop should not create state.json.
	p.Start(time.Hour) // long interval so tick won't fire
	p.Stop()

	_, errStat := os.Stat(filepath.Join(dir, "state.json"))
	if !os.IsNotExist(errStat) {
		t.Fatal("state.json should not exist when not dirty")
	}

	// Mark dirty, then Stop should trigger a save.
	if _, err := store.Add("dirty item", "", nil, 5); err != nil {
		t.Fatalf("Add: %v", err)
	}
	p2, errP2 := NewPersister(dir, store)
	if errP2 != nil {
		t.Fatalf("NewPersister: %v", errP2)
	}

	p2.Start(time.Hour)
	p2.MarkDirty()
	p2.Stop()

	_, errFinal := os.Stat(filepath.Join(dir, "state.json"))
	if errFinal != nil {
		t.Fatalf("state.json should exist after dirty stop: %v", errFinal)
	}
}
