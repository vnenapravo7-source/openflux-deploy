package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"time"
)

var deployRepoName = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
var deployRefName = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// A panel-only upgrade does not replace the updater or its upstream patches
// inside an existing Docker container. Refresh those files before rebuilding
// the server so a new patch can be applied at the same upstream commit.
func refreshServerTooling(updatePath, fallbackDir string) (string, error) {
	repo := env("OPENFLUX_DEPLOY_REPO", "vnenapravo7-source/openflux-deploy")
	ref := env("OPENFLUX_DEPLOY_REF", "main")
	if !deployRepoName.MatchString(repo) || !deployRefName.MatchString(ref) {
		return "", fmt.Errorf("invalid deployment repository or branch")
	}
	tmp, err := os.MkdirTemp("", "openflux-tooling-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	source := filepath.Join(tmp, "source")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "clone", "--quiet", "--depth", "1", "--single-branch", "--branch", ref, "https://github.com/"+repo+".git", source)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("download server fixes: %w: %s", err, string(out))
	}
	deploy := filepath.Join(source, "deploy")
	primaryDir := filepath.Dir(updatePath)
	return installServerToolingWithFallback(deploy, primaryDir, fallbackDir)
}

func installServerToolingWithFallback(deployDir, primaryDir, fallbackDir string) (string, error) {
	if err := installServerToolingTo(deployDir, primaryDir); err == nil {
		return filepath.Join(primaryDir, "update.sh"), nil
	} else if filepath.Clean(fallbackDir) == filepath.Clean(primaryDir) {
		return "", fmt.Errorf("install server fixes: %w", err)
	} else if fallbackErr := installServerToolingTo(deployDir, fallbackDir); fallbackErr != nil {
		return "", fmt.Errorf("install server fixes in %s: %v; writable fallback %s: %w", primaryDir, err, fallbackDir, fallbackErr)
	}
	return filepath.Join(fallbackDir, "update.sh"), nil
}

func installServerToolingTo(deployDir, dest string) error {
	for _, name := range []string{"update.sh", "patch-upstream.sh"} {
		if err := installToolingFile(filepath.Join(deployDir, name), filepath.Join(dest, name), 0755); err != nil {
			return err
		}
	}
	patches, err := filepath.Glob(filepath.Join(deployDir, "patches", "*.patch"))
	if err != nil {
		return err
	}
	if len(patches) == 0 {
		return fmt.Errorf("server patch files missing")
	}
	if err := os.MkdirAll(filepath.Join(dest, "patches"), 0755); err != nil {
		return err
	}
	for _, path := range patches {
		if err := installToolingFile(path, filepath.Join(dest, "patches", filepath.Base(path)), 0644); err != nil {
			return err
		}
	}
	return nil
}

func refreshPanelUpdateScript(updatePath, fallbackDir string) (string, error) {
	repo := env("OPENFLUX_DEPLOY_REPO", "vnenapravo7-source/openflux-deploy")
	ref := env("OPENFLUX_DEPLOY_REF", "main")
	if !deployRepoName.MatchString(repo) || !deployRefName.MatchString(ref) {
		return "", fmt.Errorf("invalid deployment repository or branch")
	}
	tmp, err := os.MkdirTemp("", "openflux-panel-tooling-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	source := filepath.Join(tmp, "source")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "clone", "--quiet", "--depth", "1", "--single-branch", "--branch", ref, "https://github.com/"+repo+".git", source)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("download panel updater: %w: %s", err, string(out))
	}
	src := filepath.Join(source, "deploy", "panel-update.sh")
	return installToolingFileWithFallback(src, "panel-update.sh", filepath.Dir(updatePath), fallbackDir, 0755)
}

func installToolingFileWithFallback(source, name, primaryDir, fallbackDir string, mode os.FileMode) (string, error) {
	primary := filepath.Join(primaryDir, name)
	if err := installToolingFile(source, primary, mode); err == nil {
		return primary, nil
	} else if filepath.Clean(fallbackDir) == filepath.Clean(primaryDir) {
		return "", fmt.Errorf("install %s: %w", name, err)
	} else if fallbackErr := installToolingFile(source, filepath.Join(fallbackDir, name), mode); fallbackErr != nil {
		return "", fmt.Errorf("install %s in %s: %v; writable fallback %s: %w", name, primaryDir, err, fallbackDir, fallbackErr)
	}
	return filepath.Join(fallbackDir, name), nil
}

func installToolingFile(source, dest string, mode os.FileMode) error {
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return err
	}
	tmp := dest + ".new"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		return err
	}
	return os.Rename(tmp, dest)
}

func (m *Manager) scheduleBoardRepair() {
	if !m.boardRepairPending() { return }
	m.mu.Lock()
	enabled := m.autoUpdate
	m.mu.Unlock()
	if !enabled {
		return
	}
	go func() {
		time.Sleep(5 * time.Second)
		if err := m.update(); err != nil {
			log.Printf("automatic Board server repair: %v", err)
		}
	}()
}

func (m *Manager) boardRepairPending() bool {
	marker := filepath.Join(filepath.Dir(m.versionPath), "server-patch-revision")
	if regularFile(marker) && revisionFromFile(marker) != "legacy" { return false }
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.connections {
		if c.Enabled && (c.Transport == "boards" || connectionHasTransport(c, "boards")) { return true }
	}
	return false
}

func connectionHasTransport(c Connection, kind string) bool {
	for _, link := range c.Transports {
		if link.Type == kind {
			return true
		}
	}
	return false
}
