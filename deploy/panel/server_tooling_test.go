package main

import (
	"bytes"
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

func TestInstallServerToolingFallsBackWhenPrimaryDestinationFails(t *testing.T) {
	root := t.TempDir()
	deploy := filepath.Join(root, "source", "deploy")
	if err := os.MkdirAll(filepath.Join(deploy, "patches"), 0755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"update.sh": "#!/bin/sh\necho update\n",
		"patch-upstream.sh": "#!/bin/sh\necho patch\n",
		"patches/fix.patch": "diff --git a/a b/a\n",
	}
	for name, content := range files {
		path := filepath.Join(deploy, name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	primary := filepath.Join(root, "read-only")
	if err := os.WriteFile(primary, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	fallback := filepath.Join(root, "state", "tooling")
	gotPath, err := installServerToolingWithFallback(deploy, primary, fallback)
	if err != nil {
		t.Fatalf("writable fallback failed: %v", err)
	}
	if gotPath != filepath.Join(fallback, "update.sh") {
		t.Fatalf("selected updater %q, want fallback updater", gotPath)
	}
	for name, want := range files {
		got, err := os.ReadFile(filepath.Join(fallback, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if string(got) != want {
			t.Fatalf("%s = %q, want %q", name, got, want)
		}
	}
}

func TestPanelUpdaterHasUnixLineEndings(t *testing.T) {
	data, err := os.ReadFile("../panel-update.sh")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte{13}) {
		t.Fatal("panel updater contains CR characters; bash may fail with $'\\r': command not found")
	}
}

func TestPanelUpdaterFallsBackFromUnavailableDirectory(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "panel-update.sh")
	if err := os.WriteFile(source, []byte("#!/bin/sh\necho safe\n"), 0644); err != nil {
		t.Fatal(err)
	}
	primary := filepath.Join(root, "read-only")
	if err := os.WriteFile(primary, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	fallback := filepath.Join(root, "state", "tooling")
	got, err := installToolingFileWithFallback(source, "panel-update.sh", primary, fallback, 0755)
	if err != nil {
		t.Fatalf("writable fallback failed: %v", err)
	}
	if got != filepath.Join(fallback, "panel-update.sh") {
		t.Fatalf("selected script %q, want fallback", got)
	}
}
