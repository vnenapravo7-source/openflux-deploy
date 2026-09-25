package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBoardRepairPending(t *testing.T) {
	dir := t.TempDir()
	m := &Manager{
		versionPath: filepath.Join(dir, "upstream-version"),
		connections: []Connection{{Config: Config{Enabled: true, Transport: "boards"}}},
	}
	if !m.boardRepairPending() { t.Fatal("Board fix should be offered before installation") }
	marker := filepath.Join(dir, "server-patch-revision")
	if err := os.WriteFile(marker, []byte("legacy\n"), 0600); err != nil { t.Fatal(err) }
	if !m.boardRepairPending() { t.Fatal("Board fix should be offered after rollback") }
	if err := os.WriteFile(marker, []byte("patched\n"), 0600); err != nil { t.Fatal(err) }
	if m.boardRepairPending() { t.Fatal("Board fix should not be offered after installation") }
}
