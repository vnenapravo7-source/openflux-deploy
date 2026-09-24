package main

import (
	"bufio"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const panelVersion = "0.1.0"

//go:embed static/*
var staticFiles embed.FS

type Config struct {
	Enabled           bool   `json:"enabled"`
	Transport         string `json:"transport"`
	URL               string `json:"url"`
	Mode              string `json:"mode"`
	Codec             string `json:"codec"`
	LocalIP           string `json:"local_ip"`
	EncryptionKeyFile string `json:"encryption_key_file"`
	Debug             bool   `json:"debug"`
	AutoUpdate        bool   `json:"auto_update"`
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
	Transport       string  `json:"transport,omitempty"`
	UpstreamVersion string  `json:"upstream_version,omitempty"`
	RX              float64 `json:"rx,omitempty"`
	TX              float64 `json:"tx,omitempty"`
	Error           string  `json:"error,omitempty"`
}

type Manager struct {
	mu          sync.Mutex
	configPath  string
	binaryPath  string
	versionPath string
	updatePath  string
	nodesPath   string
	config      Config
	cmd         *exec.Cmd
	startedAt   time.Time
	restarts    int
	lastError   string
	logs        []string
	traffic     []TrafficPoint
	updating    bool
	nodes       []Node
}

func defaults() Config {
	return Config{Transport: "yandex", Mode: "l4", Codec: "batched", AutoUpdate: true}
}

func NewManager(configPath, binaryPath, versionPath, updatePath, nodesPath string) (*Manager, error) {
	m := &Manager{configPath: configPath, binaryPath: binaryPath, versionPath: versionPath, updatePath: updatePath, nodesPath: nodesPath, config: defaults()}
	if raw, err := os.ReadFile(configPath); err == nil {
		if err := json.Unmarshal(raw, &m.config); err != nil {
			return nil, fmt.Errorf("read config: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := validateConfig(m.config); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}
	if raw, err := os.ReadFile(nodesPath); err == nil {
		if err := json.Unmarshal(raw, &m.nodes); err != nil {
			return nil, fmt.Errorf("read nodes: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	go m.sampleTraffic()
	if interval, err := time.ParseDuration(os.Getenv("OPENFLUX_AUTO_UPDATE_INTERVAL")); err == nil && interval > 0 {
		go m.autoUpdateLoop(interval)
	}
	if m.config.Enabled {
		if err := m.start(); err != nil {
			m.lastError = err.Error()
		}
	}
	return m, nil
}

func (m *Manager) autoUpdateLoop(interval time.Duration) {
	delay := 20 * time.Minute
	if parsed, err := time.ParseDuration(os.Getenv("OPENFLUX_AUTO_UPDATE_DELAY")); err == nil && parsed >= 0 {
		delay = parsed
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	<-timer.C
	for {
		m.mu.Lock()
		enabled := m.config.AutoUpdate
		m.mu.Unlock()
		if enabled {
			_ = m.update()
		}
		time.Sleep(interval)
	}
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

func (m *Manager) saveNodesLocked() error {
	raw, err := json.MarshalIndent(m.nodes, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(m.nodesPath), 0700); err != nil {
		return err
	}
	tmp := m.nodesPath + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0600); err != nil {
		return err
	}
	return os.Rename(tmp, m.nodesPath)
}

func (m *Manager) addNode(n Node) error {
	if err := validateNode(n); err != nil {
		return err
	}
	n.BaseURL = strings.TrimRight(n.BaseURL, "/")
	n.TLSSHA256 = normalizeFingerprint(n.TLSSHA256)
	sum := sha256.Sum256([]byte(n.BaseURL + time.Now().String()))
	n.ID = hex.EncodeToString(sum[:6])
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, existing := range m.nodes {
		if existing.BaseURL == n.BaseURL {
			return fmt.Errorf("node URL already exists")
		}
	}
	m.nodes = append(m.nodes, n)
	return m.saveNodesLocked()
}

func (m *Manager) deleteNode(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.nodes {
		if m.nodes[i].ID == id {
			m.nodes = append(m.nodes[:i], m.nodes[i+1:]...)
			return m.saveNodesLocked()
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
		tlsConfig.InsecureSkipVerify = true // Verification is replaced by the exact SHA-256 pin below.
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
			views[i] = NodeView{ID: n.ID, Name: n.Name, BaseURL: n.BaseURL}
			var state map[string]json.RawMessage
			if err := callNode(n, http.MethodGet, "/node/v1/state", &state); err != nil {
				views[i].Error = err.Error()
				return
			}
			views[i].Online = true
			_ = json.Unmarshal(state["running"], &views[i].Running)
			_ = json.Unmarshal(state["upstream_version"], &views[i].UpstreamVersion)
			var cfg Config
			_ = json.Unmarshal(state["config"], &cfg)
			views[i].Transport = cfg.Transport
			var traffic []TrafficPoint
			_ = json.Unmarshal(state["traffic"], &traffic)
			if len(traffic) > 0 {
				views[i].RX = traffic[len(traffic)-1].RX
				views[i].TX = traffic[len(traffic)-1].TX
			}
		}(i, n)
	}
	wg.Wait()
	return views
}

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
	if c.Enabled && c.Transport != "cupsonline" {
		if strings.TrimSpace(c.URL) == "" {
			return fmt.Errorf("document URL is required")
		}
		u, err := url.ParseRequestURI(c.URL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return fmt.Errorf("document URL must be an http(s) URL")
		}
	}
	return nil
}

func (m *Manager) argsLocked() []string {
	c := m.config
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

func (m *Manager) start() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.startLocked()
}

func (m *Manager) startLocked() error {
	if !m.config.Enabled || m.cmd != nil {
		return nil
	}
	cmd := exec.Command(m.binaryPath, m.argsLocked()...)
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return err
	}
	m.cmd = cmd
	m.startedAt = time.Now()
	m.lastError = ""
	m.appendLogLocked(fmt.Sprintf("[panel] OpenFlux started, pid=%d", cmd.Process.Pid))
	go m.capture(pipe)
	go m.wait(cmd)
	return nil
}

func (m *Manager) capture(r io.Reader) {
	s := bufio.NewScanner(r)
	buf := make([]byte, 64*1024)
	s.Buffer(buf, 1024*1024)
	for s.Scan() {
		m.mu.Lock()
		m.appendLogLocked(s.Text())
		m.mu.Unlock()
	}
}

func (m *Manager) wait(cmd *exec.Cmd) {
	err := cmd.Wait()
	m.mu.Lock()
	if m.cmd != cmd {
		m.mu.Unlock()
		return
	}
	m.cmd = nil
	if err != nil {
		m.lastError = err.Error()
		m.appendLogLocked("[panel] OpenFlux stopped: " + err.Error())
	} else {
		m.appendLogLocked("[panel] OpenFlux stopped")
	}
	shouldRestart := m.config.Enabled
	if shouldRestart {
		m.restarts++
	}
	m.mu.Unlock()
	if shouldRestart {
		time.Sleep(3 * time.Second)
		if err := m.start(); err != nil {
			m.mu.Lock()
			m.lastError = err.Error()
			m.mu.Unlock()
		}
	}
}

func (m *Manager) appendLogLocked(line string) {
	line = time.Now().Format("15:04:05") + "  " + line
	m.logs = append(m.logs, line)
	if len(m.logs) > 250 {
		m.logs = append([]string(nil), m.logs[len(m.logs)-250:]...)
	}
}

func (m *Manager) restart() error {
	m.mu.Lock()
	old := m.cmd
	if old != nil {
		m.cmd = nil
		_ = old.Process.Kill()
		m.appendLogLocked("[panel] restart requested")
	}
	m.restarts++
	err := m.startLocked()
	m.mu.Unlock()
	return err
}

func (m *Manager) save(c Config) error {
	if err := validateConfig(c); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(m.configPath), 0700); err != nil {
		return err
	}
	tmp := m.configPath + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0600); err != nil {
		return err
	}
	if err := os.Rename(tmp, m.configPath); err != nil {
		return err
	}
	m.mu.Lock()
	m.config = c
	m.mu.Unlock()
	return m.restart()
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
		line := strings.TrimSpace(s.Text())
		if !strings.Contains(line, ":") {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if strings.TrimSpace(parts[0]) == "lo" {
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
	var oldRX, oldTX uint64
	oldAt := time.Now()
	oldRX, oldTX, _ = readNetworkTotals()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for now := range ticker.C {
		rx, tx, err := readNetworkTotals()
		if err != nil {
			continue
		}
		seconds := now.Sub(oldAt).Seconds()
		point := TrafficPoint{At: now.Unix()}
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

func (m *Manager) state() map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	running, pid, uptime := false, 0, int64(0)
	if m.cmd != nil && m.cmd.Process != nil {
		running = true
		pid = m.cmd.Process.Pid
		uptime = int64(time.Since(m.startedAt).Seconds())
	}
	return map[string]any{
		"config": m.config, "running": running, "pid": pid, "uptime": uptime,
		"restarts": m.restarts, "last_error": m.lastError, "logs": append([]string{}, m.logs...),
		"traffic": append([]TrafficPoint{}, m.traffic...), "upstream_version": m.version(),
		"panel_version": panelVersion, "updating": m.updating,
	}
}

func (m *Manager) update() error {
	m.mu.Lock()
	if m.updating {
		m.mu.Unlock()
		return fmt.Errorf("update already running")
	}
	m.updating = true
	m.appendLogLocked("[panel] update requested")
	m.mu.Unlock()
	go func() {
		cmd := exec.Command(m.updatePath, "--force")
		out, err := cmd.CombinedOutput()
		m.mu.Lock()
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if line != "" {
				m.appendLogLocked("[update] " + line)
			}
		}
		if err != nil {
			m.lastError = "update: " + err.Error()
		}
		m.updating = false
		m.mu.Unlock()
	}()
	return nil
}

type server struct {
	mgr       *Manager
	user      string
	pass      string
	nodeToken string
}

func (s *server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/node/v1/") {
			provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if s.nodeToken == "" || subtle.ConstantTimeCompare([]byte(provided), []byte(s.nodeToken)) != 1 {
				http.Error(w, "invalid node token", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		u, p, ok := r.BasicAuth()
		userOK := subtle.ConstantTimeCompare([]byte(u), []byte(s.user)) == 1
		passOK := subtle.ConstantTimeCompare([]byte(p), []byte(s.pass)) == 1
		if !ok || !userOK || !passOK {
			w.Header().Set("WWW-Authenticate", `Basic realm="OpenFlux Control"`)
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func actionAllowed(r *http.Request) bool {
	return r.Header.Get("X-OpenFlux-Action") == "1"
}

func (s *server) api(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/state":
		state := s.mgr.state()
		state["nodes"] = s.mgr.nodeViews()
		writeJSON(w, http.StatusOK, state)
	case r.Method == http.MethodPost && r.URL.Path == "/api/nodes":
		if !actionAllowed(r) || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "action header required"})
			return
		}
		var n Node
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&n); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if err := s.mgr.addNode(n); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusCreated, map[string]bool{"ok": true})
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/api/nodes/"):
		if !actionAllowed(r) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "action header required"})
			return
		}
		if err := s.mgr.deleteNode(strings.TrimPrefix(r.URL.Path, "/api/nodes/")); err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "node not found"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/nodes/"):
		if !actionAllowed(r) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "action header required"})
			return
		}
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/nodes/"), "/")
		if len(parts) != 2 || (parts[1] != "restart" && parts[1] != "update") {
			http.NotFound(w, r)
			return
		}
		var selected *Node
		for _, n := range s.mgr.nodesSnapshot() {
			if n.ID == parts[0] {
				copy := n
				selected = &copy
				break
			}
		}
		if selected == nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "node not found"})
			return
		}
		if err := callNode(*selected, http.MethodPost, "/node/v1/"+parts[1], nil); err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]bool{"ok": true})
	case r.Method == http.MethodPut && r.URL.Path == "/api/config":
		if !actionAllowed(r) || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "action header required"})
			return
		}
		var c Config
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&c); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if err := s.mgr.save(c); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	case r.Method == http.MethodPost && r.URL.Path == "/api/restart":
		if !actionAllowed(r) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "action header required"})
			return
		}
		if err := s.mgr.restart(); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	case r.Method == http.MethodPost && r.URL.Path == "/api/update":
		if !actionAllowed(r) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "action header required"})
			return
		}
		if err := s.mgr.update(); err != nil {
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]bool{"ok": true})
	default:
		http.NotFound(w, r)
	}
}

func (s *server) nodeAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && r.URL.Path == "/node/v1/state" {
		writeJSON(w, http.StatusOK, s.mgr.state())
		return
	}
	if r.Method == http.MethodPost && r.URL.Path == "/node/v1/restart" && actionAllowed(r) {
		if err := s.mgr.restart(); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]bool{"ok": true})
		return
	}
	if r.Method == http.MethodPost && r.URL.Path == "/node/v1/update" && actionAllowed(r) {
		if err := s.mgr.update(); err != nil {
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]bool{"ok": true})
		return
	}
	http.NotFound(w, r)
}

func env(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func main() {
	user := env("OPENFLUX_ADMIN_USER", "admin")
	pass := os.Getenv("OPENFLUX_ADMIN_PASSWORD")
	if pass == "" {
		log.Fatal("OPENFLUX_ADMIN_PASSWORD must be set")
	}
	mgr, err := NewManager(
		env("OPENFLUX_CONFIG", "/etc/openflux-deploy/config.json"),
		env("OPENFLUX_BINARY", "/usr/local/bin/openflux"),
		env("OPENFLUX_VERSION_FILE", "/var/lib/openflux-deploy/upstream-version"),
		env("OPENFLUX_UPDATE_SCRIPT", "/usr/local/lib/openflux-deploy/update.sh"),
		env("OPENFLUX_NODES", "/etc/openflux-deploy/nodes.json"),
	)
	if err != nil {
		log.Fatal(err)
	}
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	go func() {
		for range hup {
			if err := mgr.restart(); err != nil {
				log.Printf("restart after update: %v", err)
			}
		}
	}()
	web, err := fs.Sub(staticFiles, "static")
	if err != nil {
		log.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, http.StatusOK, map[string]bool{"ok": true}) })
	srv := &server{mgr: mgr, user: user, pass: pass, nodeToken: os.Getenv("OPENFLUX_NODE_TOKEN")}
	mux.HandleFunc("/api/", srv.api)
	mux.HandleFunc("/node/v1/", srv.nodeAPI)
	mux.Handle("/", http.FileServer(http.FS(web)))
	h := srv.auth(mux)
	addr := env("OPENFLUX_LISTEN", ":8088")
	httpServer := &http.Server{Addr: addr, Handler: h, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
	cert, key := os.Getenv("OPENFLUX_TLS_CERT"), os.Getenv("OPENFLUX_TLS_KEY")
	log.Printf("OpenFlux panel %s listening on %s", panelVersion, addr)
	if cert != "" && key != "" {
		log.Fatal(httpServer.ListenAndServeTLS(cert, key))
	}
	log.Fatal(httpServer.ListenAndServe())
}
