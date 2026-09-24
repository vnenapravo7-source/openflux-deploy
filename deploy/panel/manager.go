package main

import (
	"bufio"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Config struct {
	Enabled           bool   `json:"enabled"`
	Transport         string `json:"transport"`
	URL               string `json:"url"`
	Mode              string `json:"mode"`
	Codec             string `json:"codec"`
	LocalIP           string `json:"local_ip"`
	EncryptionKeyFile string `json:"encryption_key_file"`
	Debug             bool   `json:"debug"`
	AutoUpdate        bool   `json:"auto_update,omitempty"` // Only used to migrate the original single connection.
}

type Connection struct {
	ID      string `json:"id"`
	OwnerID string `json:"owner_id"`
	Name    string `json:"name"`
	Config
}

type ConnectionView struct {
	Connection
	Running    bool     `json:"running"`
	PID        int      `json:"pid"`
	Uptime     int64    `json:"uptime"`
	Restarts   int      `json:"restarts"`
	LastError  string   `json:"last_error"`
	Logs       []string `json:"logs"`
	ClientCode string   `json:"client_code,omitempty"`
}

type TrafficPoint struct {
	At int64   `json:"at"`
	RX float64 `json:"rx"`
	TX float64 `json:"tx"`
}

type Node struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	BaseURL   string `json:"base_url"`
	Token     string `json:"token,omitempty"`
	TLSSHA256 string `json:"tls_sha256,omitempty"`
}

type NodeView struct {
	ID              string  `json:"id"`
	Name            string  `json:"name"`
	BaseURL         string  `json:"base_url"`
	Online          bool    `json:"online"`
	Running         bool    `json:"running"`
	Connections     int     `json:"connections"`
	UpstreamVersion string  `json:"upstream_version,omitempty"`
	RX              float64 `json:"rx,omitempty"`
	TX              float64 `json:"tx,omitempty"`
	Error           string  `json:"error,omitempty"`
}

type processState struct {
	cmd            *exec.Cmd
	startedAt      time.Time
	restarts       int
	lastError      string
	logs           []string
	clientCode     string
	expectCupsCode bool
}

type Manager struct {
	mu                 sync.Mutex
	legacyPath         string
	connectionsPath    string
	nodesPath          string
	binaryPath         string
	versionPath        string
	updatePath         string
	panelUpdatePath    string
	panelRevisionPath  string
	panelUpdateLogPath string
	connections        []Connection
	processes          map[string]*processState
	nodes              []Node
	traffic            []TrafficPoint
	autoUpdate         bool
	updating           bool
	updateError        string
	updatingPanel      bool
	panelUpdateError   string
}

func defaults() Config { return Config{Transport: "yandex", Mode: "l4", Codec: "batched"} }

func validateConfig(c Config) error {
	switch c.Transport {
	case "yandex", "vyandex", "mailru", "cupsonline":
	default:
		return fmt.Errorf("unsupported transport %q", c.Transport)
	}
	if c.Mode != "l3" && c.Mode != "l4" {
		return fmt.Errorf("mode must be l3 or l4")
	}
	if c.Codec != "batched" && c.Codec != "legacy" {
		return fmt.Errorf("codec must be batched or legacy")
	}
	if c.LocalIP != "" && net.ParseIP(c.LocalIP) == nil {
		return fmt.Errorf("local IP is invalid")
	}
	if c.Enabled && c.Transport != "cupsonline" {
		if strings.TrimSpace(c.URL) == "" {
			return fmt.Errorf("document URL is required")
		}
		u, err := url.ParseRequestURI(c.URL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil {
			return fmt.Errorf("document URL must be an http(s) URL")
		}
	}
	return nil
}

func validateConnection(c Connection) error {
	if len(strings.TrimSpace(c.Name)) < 1 || len(c.Name) > 80 {
		return fmt.Errorf("connection name must contain 1–80 characters")
	}
	return validateConfig(c.Config)
}

func NewManager(legacyPath, connectionsPath, nodesPath, binaryPath, versionPath, updatePath, adminID string) (*Manager, error) {
	m := &Manager{legacyPath: legacyPath, connectionsPath: connectionsPath, nodesPath: nodesPath, binaryPath: binaryPath, versionPath: versionPath, updatePath: updatePath, panelUpdatePath: env("OPENFLUX_PANEL_UPDATE_SCRIPT", "/usr/local/lib/openflux-deploy/panel-update.sh"), panelRevisionPath: env("OPENFLUX_PANEL_REVISION_FILE", "/var/lib/openflux-deploy/panel-revision"), panelUpdateLogPath: env("OPENFLUX_PANEL_UPDATE_LOG", "/var/lib/openflux-deploy/panel-update.log"), processes: map[string]*processState{}, nodes: []Node{}, connections: []Connection{}, traffic: []TrafficPoint{}, autoUpdate: true}
	if raw, err := os.ReadFile(legacyPath); err == nil {
		var old Config
		if err := json.Unmarshal(raw, &old); err != nil {
			return nil, fmt.Errorf("legacy config: %w", err)
		}
		m.autoUpdate = old.AutoUpdate
		if _, err := os.Stat(connectionsPath); errors.Is(err, os.ErrNotExist) && (old.Enabled || old.URL != "") {
			id, err := newID()
			if err != nil {
				return nil, err
			}
			old.AutoUpdate = false
			m.connections = append(m.connections, Connection{ID: id, OwnerID: adminID, Name: "Основное", Config: old})
			if err := writeJSONFile(connectionsPath, m.connections); err != nil {
				return nil, fmt.Errorf("migrate connection: %w", err)
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if raw, err := os.ReadFile(connectionsPath); err == nil {
		if err := json.Unmarshal(raw, &m.connections); err != nil {
			return nil, fmt.Errorf("connections file: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	for _, c := range m.connections {
		if err := validateConnection(c); err != nil {
			return nil, fmt.Errorf("connection %s: %w", c.ID, err)
		}
		m.processes[c.ID] = &processState{}
	}
	if raw, err := os.ReadFile(nodesPath); err == nil {
		if err := json.Unmarshal(raw, &m.nodes); err != nil {
			return nil, fmt.Errorf("nodes file: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	for _, c := range m.connections {
		if c.Enabled {
			m.mu.Lock()
			err := m.startLocked(c.ID)
			m.mu.Unlock()
			if err != nil {
				m.mu.Lock()
				m.processes[c.ID].lastError = err.Error()
				m.mu.Unlock()
			}
		}
	}
	go m.sampleTraffic()
	if interval, err := time.ParseDuration(os.Getenv("OPENFLUX_AUTO_UPDATE_INTERVAL")); err == nil && interval > 0 {
		go m.autoUpdateLoop(interval)
	}
	return m, nil
}

func (m *Manager) autoUpdateLoop(interval time.Duration) {
	delay := 20 * time.Minute
	if d, err := time.ParseDuration(os.Getenv("OPENFLUX_AUTO_UPDATE_DELAY")); err == nil && d >= 0 {
		delay = d
	}
	time.Sleep(delay)
	for {
		m.mu.Lock()
		enabled := m.autoUpdate
		m.mu.Unlock()
		if enabled {
			_ = m.update()
		}
		time.Sleep(interval)
	}
}

func (m *Manager) findLocked(id string) (int, bool) {
	for i := range m.connections {
		if m.connections[i].ID == id {
			return i, true
		}
	}
	return -1, false
}

func connectionArgs(c Connection) []string {
	args := []string{"--role=exit", "--mode=" + c.Mode, "--transport=" + c.Transport, "--codec=" + c.Codec}
	if c.URL != "" {
		args = append(args, "--url="+c.URL)
	}
	if c.LocalIP != "" {
		args = append(args, "--local-ip="+c.LocalIP)
	}
	if c.EncryptionKeyFile != "" {
		args = append(args, "--encryption-key-file="+c.EncryptionKeyFile)
	}
	if c.Debug {
		args = append(args, "--debug")
	}
	return args
}

func (m *Manager) startLocked(id string) error {
	i, ok := m.findLocked(id)
	if !ok {
		return os.ErrNotExist
	}
	c := m.connections[i]
	p := m.processes[id]
	if !c.Enabled || p.cmd != nil {
		return nil
	}
	cmd := exec.Command(m.binaryPath, connectionArgs(c)...)
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return err
	}
	p.cmd, p.startedAt, p.lastError = cmd, time.Now(), ""
	p.clientCode, p.expectCupsCode = "", false
	m.logLocked(id, fmt.Sprintf("[panel] started %s, pid=%d", c.Name, cmd.Process.Pid))
	go m.capture(id, pipe)
	go m.wait(id, cmd)
	return nil
}

func (m *Manager) capture(id string, r io.Reader) {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 64*1024), 1024*1024)
	for s.Scan() {
		m.mu.Lock()
		m.logLocked(id, s.Text())
		m.mu.Unlock()
	}
}

func (m *Manager) logLocked(id, line string) {
	p := m.processes[id]
	if p == nil {
		return
	}
	if strings.Contains(line, "=== COPY THIS TO CLIENT ===") {
		p.expectCupsCode = true
	} else if p.expectCupsCode {
		candidate := strings.TrimSpace(line)
		if candidate == "" { // fmt.Printf starts the marker with a newline.
		} else if validCupsCode(candidate) {
			p.clientCode = candidate
			p.expectCupsCode = false
		} else {
			p.expectCupsCode = false
		}
	}
	p.logs = append(p.logs, time.Now().Format("15:04:05")+"  "+line)
	if len(p.logs) > 200 {
		p.logs = append([]string(nil), p.logs[len(p.logs)-200:]...)
	}
}

func validCupsCode(value string) bool {
	if len(value) > 8192 {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return false
	}
	var ids []string
	if err := json.Unmarshal(raw, &ids); err != nil || len(ids) == 0 {
		return false
	}
	for _, id := range ids {
		if len(id) != 36 {
			return false
		}
	}
	return true
}

func (m *Manager) wait(id string, cmd *exec.Cmd) {
	err := cmd.Wait()
	m.mu.Lock()
	p := m.processes[id]
	if p == nil || p.cmd != cmd {
		m.mu.Unlock()
		return
	}
	p.cmd = nil
	if err != nil {
		p.lastError = err.Error()
		m.logLocked(id, "[panel] stopped: "+err.Error())
	} else {
		m.logLocked(id, "[panel] stopped")
	}
	i, ok := m.findLocked(id)
	restart := ok && m.connections[i].Enabled
	if restart {
		p.restarts++
	}
	m.mu.Unlock()
	if restart {
		time.Sleep(3 * time.Second)
		m.mu.Lock()
		if e := m.startLocked(id); e != nil {
			if p := m.processes[id]; p != nil {
				p.lastError = e.Error()
			}
		}
		m.mu.Unlock()
	}
}

func (m *Manager) restartLocked(id string) error {
	i, ok := m.findLocked(id)
	if !ok {
		return os.ErrNotExist
	}
	p := m.processes[id]
	if p.cmd != nil {
		old := p.cmd
		p.cmd = nil
		_ = old.Process.Kill()
	}
	p.restarts++
	if !m.connections[i].Enabled {
		p.lastError = ""
		return nil
	}
	if err := m.startLocked(id); err != nil {
		p.lastError = err.Error()
		return err
	}
	return nil
}

func (m *Manager) restart(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.restartLocked(id)
}

func (m *Manager) restartAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.connections {
		if err := m.restartLocked(c.ID); err != nil {
			m.logLocked(c.ID, "[panel] restart failed: "+err.Error())
		}
	}
}

func (m *Manager) addConnection(c Connection) (Connection, error) {
	if err := validateConnection(c); err != nil {
		return Connection{}, err
	}
	id, err := newID()
	if err != nil {
		return Connection{}, err
	}
	c.ID = id
	c.AutoUpdate = false
	m.mu.Lock()
	defer m.mu.Unlock()
	next := append(append([]Connection(nil), m.connections...), c)
	if err := writeJSONFile(m.connectionsPath, next); err != nil {
		return Connection{}, err
	}
	m.connections = next
	m.processes[id] = &processState{}
	if err := m.startLocked(id); err != nil {
		m.processes[id].lastError = err.Error()
	}
	return c, nil
}

func (m *Manager) updateConnection(id string, c Connection) error {
	if err := validateConnection(c); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	i, ok := m.findLocked(id)
	if !ok {
		return os.ErrNotExist
	}
	c.ID, c.OwnerID, c.AutoUpdate = id, m.connections[i].OwnerID, false
	next := append([]Connection(nil), m.connections...)
	next[i] = c
	if err := writeJSONFile(m.connectionsPath, next); err != nil {
		return err
	}
	m.connections = next
	return m.restartLocked(id)
}

func (m *Manager) deleteConnection(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	i, ok := m.findLocked(id)
	if !ok {
		return os.ErrNotExist
	}
	next := append([]Connection(nil), m.connections[:i]...)
	next = append(next, m.connections[i+1:]...)
	if err := writeJSONFile(m.connectionsPath, next); err != nil {
		return err
	}
	if p := m.processes[id]; p != nil && p.cmd != nil {
		p.cmd.Process.Kill()
	}
	delete(m.processes, id)
	m.connections = next
	return nil
}

func (m *Manager) connection(id string) (Connection, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	i, ok := m.findLocked(id)
	if !ok {
		return Connection{}, false
	}
	return m.connections[i], true
}

func (m *Manager) connectionViews(user User) []ConnectionView {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]ConnectionView, 0, len(m.connections))
	for _, c := range m.connections {
		if user.Role != "admin" && c.OwnerID != user.ID {
			continue
		}
		v := ConnectionView{Connection: c, Logs: []string{}}
		if p := m.processes[c.ID]; p != nil {
			v.Restarts, v.LastError, v.Logs, v.ClientCode = p.restarts, p.lastError, append([]string{}, p.logs...), p.clientCode
			if p.cmd != nil {
				v.Running, v.PID, v.Uptime = true, p.cmd.Process.Pid, int64(time.Since(p.startedAt).Seconds())
			}
		}
		out = append(out, v)
	}
	return out
}

func (m *Manager) health() map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	enabled, running := 0, 0
	for _, c := range m.connections {
		if c.Enabled {
			enabled++
			if p := m.processes[c.ID]; p != nil && p.cmd != nil {
				running++
			}
		}
	}
	return map[string]any{"ok": true, "enabled": enabled, "running": running}
}

func readNetworkTotals() (uint64, uint64, error) {
	f, err := os.Open("/proc/net/dev")
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	var rx, tx uint64
	s := bufio.NewScanner(f)
	for s.Scan() {
		parts := strings.SplitN(strings.TrimSpace(s.Text()), ":", 2)
		if len(parts) != 2 || strings.TrimSpace(parts[0]) == "lo" {
			continue
		}
		fields := strings.Fields(parts[1])
		if len(fields) < 9 {
			continue
		}
		r, er := strconv.ParseUint(fields[0], 10, 64)
		t, et := strconv.ParseUint(fields[8], 10, 64)
		if er == nil && et == nil {
			rx += r
			tx += t
		}
	}
	return rx, tx, s.Err()
}

func (m *Manager) sampleTraffic() {
	oldRX, oldTX, _ := readNetworkTotals()
	oldAt := time.Now()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for now := range ticker.C {
		rx, tx, err := readNetworkTotals()
		if err != nil {
			continue
		}
		point := TrafficPoint{At: now.Unix()}
		seconds := now.Sub(oldAt).Seconds()
		if rx >= oldRX && tx >= oldTX && seconds > 0 {
			point.RX = float64(rx-oldRX) / seconds
			point.TX = float64(tx-oldTX) / seconds
		}
		oldRX, oldTX, oldAt = rx, tx, now
		m.mu.Lock()
		m.traffic = append(m.traffic, point)
		if len(m.traffic) > 120 {
			m.traffic = append([]TrafficPoint(nil), m.traffic[len(m.traffic)-120:]...)
		}
		m.mu.Unlock()
	}
}

func (m *Manager) version() string {
	raw, err := os.ReadFile(m.versionPath)
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(raw))
}

func (m *Manager) panelRevision() string {
	if m.panelRevisionPath == "" {
		return "unknown"
	}
	raw, err := os.ReadFile(m.panelRevisionPath)
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(raw))
}

func (m *Manager) state(user User) map[string]any {
	state := map[string]any{"me": UserView{user.ID, user.Username, user.Role}, "connections": m.connectionViews(user), "upstream_version": m.version(), "panel_version": panelVersion, "panel_revision": m.panelRevision()}
	if user.Role == "admin" {
		m.mu.Lock()
		state["traffic"] = append([]TrafficPoint{}, m.traffic...)
		state["auto_update"] = m.autoUpdate
		state["updating"] = m.updating
		state["update_error"] = m.updateError
		state["updating_panel"] = m.updatingPanel
		state["panel_update_error"] = m.panelUpdateError
		m.mu.Unlock()
		state["panel_update_log"] = tailFile(m.panelUpdateLogPath, 8192)
		state["nodes"] = m.nodeViews()
	}
	return state
}

func (m *Manager) setAutoUpdate(value bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	legacy := defaults()
	legacy.AutoUpdate = value
	if err := writeJSONFile(m.legacyPath, legacy); err != nil {
		return err
	}
	m.autoUpdate = value
	return nil
}

func (m *Manager) update() error {
	m.mu.Lock()
	if m.updating || m.updatingPanel {
		m.mu.Unlock()
		return fmt.Errorf("update already running")
	}
	m.updating = true
	m.updateError = ""
	m.mu.Unlock()
	go func() {
		cmd := exec.Command(m.updatePath, "--force")
		out, err := cmd.CombinedOutput()
		m.mu.Lock()
		if err != nil {
			m.updateError = fmt.Sprintf("%v: %s", err, strings.TrimSpace(string(out)))
		}
		m.updating = false
		m.mu.Unlock()
	}()
	return nil
}

func (m *Manager) updatePanel() error {
	m.mu.Lock()
	if m.updating || m.updatingPanel {
		m.mu.Unlock()
		return fmt.Errorf("update already running")
	}
	m.updatingPanel = true
	m.panelUpdateError = ""
	m.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(m.panelUpdateLogPath), 0700); err != nil {
		m.mu.Lock()
		m.updatingPanel = false
		m.panelUpdateError = err.Error()
		m.mu.Unlock()
		return err
	}
	logFile, err := os.OpenFile(m.panelUpdateLogPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		m.mu.Lock()
		m.updatingPanel = false
		m.panelUpdateError = err.Error()
		m.mu.Unlock()
		return err
	}
	cmd := exec.Command(m.panelUpdatePath, "--force")
	cmd.Env = append(os.Environ(), fmt.Sprintf("OPENFLUX_PANEL_PID=%d", os.Getpid()))
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		logFile.Close()
		m.mu.Lock()
		m.updatingPanel = false
		m.panelUpdateError = err.Error()
		m.mu.Unlock()
		return err
	}
	go func() {
		err := cmd.Wait()
		logFile.Close()
		m.mu.Lock()
		if err != nil {
			m.panelUpdateError = err.Error()
		}
		m.updatingPanel = false
		m.mu.Unlock()
	}()
	return nil
}

func tailFile(path string, limit int64) string {
	if path == "" || limit <= 0 {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return ""
	}
	if info.Size() > limit {
		if _, err := f.Seek(info.Size()-limit, io.SeekStart); err != nil {
			return ""
		}
	}
	raw, _ := io.ReadAll(io.LimitReader(f, limit))
	return strings.ToValidUTF8(string(raw), "")
}

func normalizeFingerprint(v string) string {
	return strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(v), ":", ""), " ", ""))
}

func validateNode(n Node) error {
	if strings.TrimSpace(n.Name) == "" || strings.TrimSpace(n.Token) == "" {
		return fmt.Errorf("node name and token are required")
	}
	u, err := url.ParseRequestURI(n.BaseURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return fmt.Errorf("node URL must be an https URL without credentials")
	}
	if fp := normalizeFingerprint(n.TLSSHA256); fp != "" {
		if len(fp) != 64 {
			return fmt.Errorf("TLS SHA-256 fingerprint must contain 64 hex characters")
		}
		if _, err := hex.DecodeString(fp); err != nil {
			return fmt.Errorf("invalid TLS SHA-256 fingerprint")
		}
	}
	return nil
}

func (m *Manager) addNode(n Node) error {
	if err := validateNode(n); err != nil {
		return err
	}
	n.BaseURL = strings.TrimRight(n.BaseURL, "/")
	n.TLSSHA256 = normalizeFingerprint(n.TLSSHA256)
	id, err := newID()
	if err != nil {
		return err
	}
	n.ID = id
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, existing := range m.nodes {
		if existing.BaseURL == n.BaseURL {
			return fmt.Errorf("node URL already exists")
		}
	}
	next := append(append([]Node(nil), m.nodes...), n)
	if err := writeJSONFile(m.nodesPath, next); err != nil {
		return err
	}
	m.nodes = next
	return nil
}

func (m *Manager) deleteNode(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.nodes {
		if m.nodes[i].ID == id {
			next := append([]Node(nil), m.nodes[:i]...)
			next = append(next, m.nodes[i+1:]...)
			if err := writeJSONFile(m.nodesPath, next); err != nil {
				return err
			}
			m.nodes = next
			return nil
		}
	}
	return os.ErrNotExist
}

func (m *Manager) nodesSnapshot() []Node {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Node(nil), m.nodes...)
}

func nodeClient(n Node) *http.Client {
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if fp := normalizeFingerprint(n.TLSSHA256); fp != "" {
		tlsConfig.InsecureSkipVerify = true // Exact certificate pin replaces default verification.
		tlsConfig.VerifyConnection = func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return fmt.Errorf("node returned no certificate")
			}
			sum := sha256.Sum256(cs.PeerCertificates[0].Raw)
			if hex.EncodeToString(sum[:]) != fp {
				return fmt.Errorf("TLS certificate fingerprint mismatch")
			}
			return nil
		}
	}
	return &http.Client{Timeout: 8 * time.Second, Transport: &http.Transport{TLSClientConfig: tlsConfig}}
}

func callNode(n Node, method, path string, out any) error {
	req, err := http.NewRequest(method, n.BaseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+n.Token)
	req.Header.Set("X-OpenFlux-Action", "1")
	resp, err := nodeClient(n).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("node returned HTTP %d", resp.StatusCode)
	}
	if out != nil {
		return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
	}
	return nil
}

func (m *Manager) nodeViews() []NodeView {
	nodes := m.nodesSnapshot()
	views := make([]NodeView, len(nodes))
	var wg sync.WaitGroup
	for i, n := range nodes {
		wg.Add(1)
		go func(i int, n Node) {
			defer wg.Done()
			v := NodeView{ID: n.ID, Name: n.Name, BaseURL: n.BaseURL}
			var state struct {
				Running         int            `json:"running"`
				Connections     int            `json:"connections"`
				UpstreamVersion string         `json:"upstream_version"`
				Traffic         []TrafficPoint `json:"traffic"`
			}
			if err := callNode(n, http.MethodGet, "/node/v1/state", &state); err != nil {
				v.Error = err.Error()
			} else {
				v.Online = true
				v.Running = state.Running > 0
				v.Connections = state.Connections
				v.UpstreamVersion = state.UpstreamVersion
				if len(state.Traffic) > 0 {
					v.RX = state.Traffic[len(state.Traffic)-1].RX
					v.TX = state.Traffic[len(state.Traffic)-1].TX
				}
			}
			views[i] = v
		}(i, n)
	}
	wg.Wait()
	return views
}

func (m *Manager) nodeState() map[string]any {
	h := m.health()
	m.mu.Lock()
	traffic := append([]TrafficPoint{}, m.traffic...)
	count := len(m.connections)
	m.mu.Unlock()
	return map[string]any{"running": h["running"], "connections": count, "traffic": traffic, "upstream_version": m.version()}
}

func (m *Manager) removeUserConnections(ownerID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	next := make([]Connection, 0, len(m.connections))
	for _, c := range m.connections {
		if c.OwnerID != ownerID {
			next = append(next, c)
		}
	}
	if err := writeJSONFile(m.connectionsPath, next); err != nil {
		return err
	}
	for _, c := range m.connections {
		if c.OwnerID == ownerID {
			if p := m.processes[c.ID]; p != nil && p.cmd != nil {
				_ = p.cmd.Process.Kill()
			}
			delete(m.processes, c.ID)
		}
	}
	m.connections = next
	return nil
}
