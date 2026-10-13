package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

func regularFile(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Size() > 0
}

func (m *Manager) rollbackServer() error {
	if !m.rollbackCapable {
		return fmt.Errorf("reinstall the panel service or Docker image to enable rollback")
	}
	if !regularFile(m.binaryPath+".rollback") || !regularFile(m.versionPath+".rollback") {
		return fmt.Errorf("previous server version is unavailable")
	}
	patchFile := filepath.Join(filepath.Dir(m.versionPath), "server-patch-revision")
	if revisionFromFile(m.versionPath+".rollback") == m.version() && revisionFromFile(patchFile+".rollback") == revisionFromFile(patchFile) {
		return fmt.Errorf("server backup is the same revision as the current version")
	}
	m.mu.Lock()
	if m.updating || m.updatingPanel {
		m.mu.Unlock()
		return fmt.Errorf("update or rollback already running")
	}
	m.updating = true
	m.updateError = ""
	m.serverAction = "rollback"
	m.mu.Unlock()
	go func() {
		scriptPath := m.serverUpdateScriptPath()
		cmd := exec.Command(scriptPath, "--rollback")
		cmd.Env = append(os.Environ(), "OPENFLUX_PATCH_DIR="+filepath.Dir(scriptPath))
		out, err := cmd.CombinedOutput()
		if err == nil {
			if disableErr := m.setAutoUpdate(false); disableErr != nil {
				err = fmt.Errorf("rollback completed, but automatic updates could not be disabled: %w", disableErr)
			}
		}
		m.mu.Lock()
		if err != nil {
			m.updateError = fmt.Sprintf("%v: %s", err, strings.TrimSpace(string(out)))
		}
		m.updating = false
		m.mu.Unlock()
	}()
	return nil
}

func (m *Manager) rollbackPanel() error {
	if !m.rollbackCapable {
		return fmt.Errorf("reinstall the panel service or Docker image to enable rollback")
	}
	panelBinary := filepath.Join(filepath.Dir(m.panelRevisionPath), "bin", "openflux-panel")
	if !regularFile(panelBinary+".rollback") || !regularFile(m.panelRevisionPath+".rollback") {
		return fmt.Errorf("previous panel version is unavailable")
	}
	if revisionFromFile(m.panelRevisionPath+".rollback") == m.panelRevision() {
		return fmt.Errorf("panel backup is the same revision as the current version")
	}
	return m.runPanelScript("--rollback", "rollback")
}

func remoteHead(repoURL, ref string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "ls-remote", repoURL, "refs/heads/"+ref).Output()
	if err != nil {
		return "", fmt.Errorf("remote version check failed: %w", err)
	}
	fields := strings.Fields(string(out))
	if len(fields) < 2 || len(fields[0]) != 40 {
		return "", fmt.Errorf("remote branch not found")
	}
	if _, err := hex.DecodeString(fields[0]); err != nil {
		return "", fmt.Errorf("invalid remote revision")
	}
	return fields[0], nil
}

// latestReleaseHead resolves the newest stable vX.Y.Z tag rather than main.
// OpenFlux publishes node fixes from release branches, which can be ahead of
// main for a while.  A panel must offer that published release to its users.
func latestReleaseHead(repoURL string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "ls-remote", "--tags", "--refs", repoURL, "refs/tags/v[0-9]*").Output()
	if err != nil {
		return "", fmt.Errorf("remote release check failed: %w", err)
	}
	tag, ok := latestStableTag(string(out))
	if !ok {
		return "", fmt.Errorf("no stable release tag found")
	}
	out, err = exec.CommandContext(ctx, "git", "ls-remote", repoURL, "refs/tags/"+tag+"^{}", "refs/tags/"+tag).Output()
	if err != nil {
		return "", fmt.Errorf("remote release lookup failed: %w", err)
	}
	var fallback string
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || len(fields[0]) != 40 {
			continue
		}
		if _, err := hex.DecodeString(fields[0]); err != nil {
			continue
		}
		if strings.HasSuffix(fields[1], "^{}") {
			return fields[0], nil // annotated tag: use the peeled commit
		}
		fallback = fields[0]
	}
	if fallback == "" {
		return "", fmt.Errorf("release tag %s not found", tag)
	}
	return fallback, nil
}

func latestStableTag(output string) (string, bool) {
	type candidate struct {
		tag     string
		version []int
	}
	var candidates []candidate
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || !strings.HasPrefix(fields[1], "refs/tags/v") {
			continue
		}
		tag := strings.TrimPrefix(fields[1], "refs/tags/")
		parts := strings.Split(strings.TrimPrefix(tag, "v"), ".")
		version := make([]int, len(parts))
		valid := len(parts) > 0
		for i, part := range parts {
			if part == "" {
				valid = false
				break
			}
			n, err := strconv.Atoi(part)
			if err != nil || n < 0 {
				valid = false
				break
			}
			version[i] = n
		}
		if valid {
			candidates = append(candidates, candidate{tag: tag, version: version})
		}
	}
	if len(candidates) == 0 {
		return "", false
	}
	sort.Slice(candidates, func(i, j int) bool {
		for n := 0; n < len(candidates[i].version) || n < len(candidates[j].version); n++ {
			var left, right int
			if n < len(candidates[i].version) {
				left = candidates[i].version[n]
			}
			if n < len(candidates[j].version) {
				right = candidates[j].version[n]
			}
			if left != right {
				return left < right
			}
		}
		return candidates[i].tag < candidates[j].tag
	})
	return candidates[len(candidates)-1].tag, true
}

func (m *Manager) checkVersions(force bool) {
	m.mu.Lock()
	if m.checkingVersions || (!force && !m.lastVersionCheck.IsZero() && time.Since(m.lastVersionCheck) < 10*time.Minute) {
		m.mu.Unlock()
		return
	}
	m.checkingVersions = true
	m.versionCheckError = ""
	m.lastVersionCheck = time.Now()
	m.mu.Unlock()
	go func() {
		repo, ref := env("OPENFLUX_DEPLOY_REPO", "vnenapravo7-source/openflux-deploy"), env("OPENFLUX_DEPLOY_REF", "main")
		type result struct {
			name string
			sha  string
			err  error
		}
		results := make(chan result, 2)
		go func() {
			sha, err := latestReleaseHead(env("OPENFLUX_UPSTREAM_REPO", "https://github.com/p1neappleXpress/OpenFlux.git"))
			results <- result{name: "upstream", sha: sha, err: err}
		}()
		go func() {
			sha, err := remoteHead("https://github.com/"+repo+".git", ref)
			results <- result{name: "panel", sha: sha, err: err}
		}()
		first, second := <-results, <-results
		var upstream, panel string
		var upstreamErr, panelErr error
		for _, item := range []result{first, second} {
			if item.name == "upstream" {
				upstream, upstreamErr = item.sha, item.err
			} else {
				panel, panelErr = item.sha, item.err
			}
		}
		m.mu.Lock()
		m.latestUpstream = upstream
		m.latestPanel = panel
		var problems []string
		if upstreamErr != nil {
			problems = append(problems, "сервер: "+upstreamErr.Error())
		}
		if panelErr != nil {
			problems = append(problems, "панель: "+panelErr.Error())
		}
		m.versionCheckError = strings.Join(problems, "; ")
		m.checkingVersions = false
		m.mu.Unlock()
	}()
}
