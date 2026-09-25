package main

import (
	"bufio"
	"crypto/rand"
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
	Negotiate        bool   `json:"negotiate,omitempty"`
	SessionContextURL string `json:"session_context_url,omitempty"`
	Transports        []TransportLink `json:"transports,omitempty"`
	DirectListen      string `json:"direct_listen,omitempty"`
	MaxPacketSize     int    `json:"max_packet_size,omitempty"`
	Debug             bool   `json:"debug"`
	AutoUpdate        bool   `json:"auto_update,omitempty"` // Only used to migrate the original single connection.
}

// TransportLink describes one carrier in the authenticated OpenFlux session.
// An empty list keeps the original single-transport protocol for old clients.
type TransportLink struct {
	Type     string `json:"type"`
	URL      string `json:"url,omitempty"`
	Priority int    `json:"priority"`
}

type Connection struct {
	ID      string `json:"id"`
	OwnerID string `json:"owner_id"`
	Name    string `json:"name"`
	Config
}

type ConnectionView struct {
	Connection
	KeyManaged bool `json:"key_managed"`
	SessionContext string `json:"session_context,omitempty"`
	YandexAuthRequired bool `json:"yandex_auth_required,omitempty"`
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
	yandexAuthRequired bool
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
	panelUpdateLogPath  string
	serverUpdateLogPath string
	rollbackCapable    bool
	connections        []Connection
	processes          map[string]*processState
	nodes              []Node
	traffic            []TrafficPoint
	autoUpdate         bool
	updating           bool
	updateError        string
	serverAction       string
	updatingPanel      bool
	panelUpdateError   string
	panelAction        string
	checkingVersions   bool
	latestUpstream     string
	latestPanel        string
	versionCheckError  string
	lastVersionCheck   time.Time
}

func defaults() Config { return Config{Transport: "yandex", Mode: "l4", Codec: "batched"} }

func validateConfig(c Config) error {
	switch c.Transport {
	case "yandex", "vyandex", "boards", "mailru", "cupsonline", "direct":
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
	if len(c.Transports) == 0 {
		if c.SessionContextURL != "" { return fmt.Errorf("session context URL requires a protected session") }
		if c.Negotiate && c.Codec != "batched" { return fmt.Errorf("negotiation requires batched codec") }
		if c.DirectListen != "" || c.MaxPacketSize != 0 {
			if c.Transport != "direct" || c.MaxPacketSize != 0 { return fmt.Errorf("session settings require at least one transport") }
		}
		if c.Transport == "direct" {
			if c.DirectListen != "" { return validateDirectListen(c.DirectListen) }
			return nil
		}
		return validateTransportURL(c.Transport, c.URL, c.Enabled)
	}
	if c.Codec != "batched" {
		return fmt.Errorf("authenticated session requires batched codec")
	}
	if c.SessionContextURL != "" {
		if err := validateTransportURL("yandex", c.SessionContextURL, true); err != nil { return fmt.Errorf("session context: %w", err) }
	}
	if c.MaxPacketSize != 0 && (c.MaxPacketSize < 1280 || c.MaxPacketSize > 65000) {
		return fmt.Errorf("maximum packet size must be 1280–65000")
	}
	if len(c.Transports) > 8 {
		return fmt.Errorf("at most 8 transports are supported")
	}
	seen := make(map[string]bool, len(c.Transports))
	direct := false
	for _, link := range c.Transports {
		switch link.Type {
		case "yandex", "vyandex", "boards", "mailru", "cupsonline", "direct":
		default:
			return fmt.Errorf("unsupported session transport %q", link.Type)
		}
		if seen[link.Type] && (link.Type == "direct" || link.Type == "cupsonline") {
			return fmt.Errorf("transport %q appears more than once", link.Type)
		}
		seen[link.Type] = true
		if link.Priority < 1 || link.Priority > 1000 {
			return fmt.Errorf("transport priority must be 1–1000")
		}
		if link.Type == "direct" {
			direct = true
			if link.URL != "" {
				return fmt.Errorf("direct transport does not use a document URL")
			}
			continue
		}
		if err := validateTransportURL(link.Type, link.URL, c.Enabled); err != nil {
			return err
		}
		if strings.ContainsAny(link.URL, "#;\n\r") { return fmt.Errorf("transport URL contains characters unsupported by OpenFlux config") }
	}
	if direct {
		if c.DirectListen != "" {
			if err := validateDirectListen(c.DirectListen); err != nil {
				return err
			}
		}
	} else if c.DirectListen != "" {
		return fmt.Errorf("direct listen address requires a direct transport")
	}
	return nil
}

func validateTransportURL(kind, value string, enabled bool) error {
	if !enabled || kind == "cupsonline" && value == "" {
		return nil
	}
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("document URL is required for %s", kind)
	}
	u, err := url.ParseRequestURI(value)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil {
		return fmt.Errorf("document URL must be an http(s) URL")
	}
	if kind == "boards" && (u.Scheme != "https" || !strings.EqualFold(u.Hostname(), "boards.yandex.ru") || strings.TrimSpace(u.Query().Get("hash")) == "") {
		return fmt.Errorf("Yandex Board requires an https://boards.yandex.ru/... URL with a hash parameter")
	}
	return nil
}

func validateDirectListen(value string) error {
	host, portText, err := net.SplitHostPort(value)
	port, parseErr := strconv.Atoi(portText)
	if err != nil || parseErr != nil || port < 1 || port > 65535 || (host != "" && host != "0.0.0.0" && host != "::") {
		return fmt.Errorf("direct listen address must be :PORT or 0.0.0.0:PORT")
	}
	if _, err := os.Stat("/.dockerenv"); err == nil && (port < 39000 || port > 39015) {
		return fmt.Errorf("Docker direct transport ports must be within 39000–39015")
	}
	return nil
}

func usesDirect(c Config) bool {
	if c.Transport == "direct" && len(c.Transports) == 0 { return true }
	for _, link := range c.Transports {
		if link.Type == "direct" {
			return true
		}
	}
	return false
}

func dockerDirectReady() bool {
	if _, err := os.Stat("/.dockerenv"); errors.Is(err, os.ErrNotExist) {
		return true // systemd installation, no Docker port publishing needed.
	}
	return os.Getenv("OPENFLUX_DIRECT_PORTS_PUBLISHED") == "1"
}

func validateConnection(c Connection) error {
	if len(strings.TrimSpace(c.Name)) < 1 || len(c.Name) > 80 {
		return fmt.Errorf("connection name must contain 1–80 characters")
	}
	return validateConfig(c.Config)
}

func NewManager(legacyPath, connectionsPath, nodesPath, binaryPath, versionPath, updatePath, adminID string) (*Manager, error) {
	m := &Manager{legacyPath: legacyPath, connectionsPath: connectionsPath, nodesPath: nodesPath, binaryPath: binaryPath, versionPath: versionPath, updatePath: updatePath, panelUpdatePath: env("OPENFLUX_PANEL_UPDATE_SCRIPT", "/usr/local/lib/openflux-deploy/panel-update.sh"), panelRevisionPath: env("OPENFLUX_PANEL_REVISION_FILE", "/var/lib/openflux-deploy/panel-revision"), panelUpdateLogPath: env("OPENFLUX_PANEL_UPDATE_LOG", "/var/lib/openflux-deploy/panel-update.log"), serverUpdateLogPath: env("OPENFLUX_SERVER_UPDATE_LOG", "/var/lib/openflux-deploy/server-update.log"), rollbackCapable: os.Getenv("OPENFLUX_ROLLBACK_CAPABLE") == "1", processes: map[string]*processState{}, nodes: []Node{}, connections: []Connection{}, traffic: []TrafficPoint{}, autoUpdate: true}
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
	migratedKeys := false
	for i := range m.connections {
		c := &m.connections[i]
		if c.EncryptionKeyFile != "" && !filepath.IsAbs(c.EncryptionKeyFile) {
			if _, err := prepareKey(c, nil); err != nil { return nil, fmt.Errorf("migrate key for %s: %w", c.ID, err) }
			migratedKeys = true
		}
		if err := validateConnection(*c); err != nil {
			return nil, fmt.Errorf("connection %s: %w", c.ID, err)
		}
		m.processes[c.ID] = &processState{}
	}
	if migratedKeys { if err := writeJSONFile(connectionsPath, m.connections); err != nil { return nil, err } }
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
	delay := 2 * time.Minute
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
	args := []string{"--role=exit", "--mode=" + c.Mode, "--codec=" + c.Codec}
	if len(c.Transports) == 0 {
		args = append(args, "--transport="+c.Transport)
		if c.Negotiate { args = append(args, "--negotiate") }
		if c.Transport == "direct" { args = append(args, "--direct-listen="+c.DirectListen) }
		if c.URL != "" {
			args = append(args, "--url="+c.URL)
		}
		if c.Transport != "direct" {
			args = append(args, "--cookie-store="+cookieStorePath(c.ID))
		}
	} else {
		if needsNamedConfig(c) {
			args = append(args, "--config="+sessionConfigPath(c.ID))
		} else {
			list := make([]string, 0, len(c.Transports))
			for _, link := range c.Transports {
				list = append(list, fmt.Sprintf("%s:%d", link.Type, link.Priority))
				if link.URL != "" { args = append(args, "--"+link.Type+"-url="+link.URL) }
			}
			args = append(args, "--transports="+strings.Join(list, ","))
			if c.DirectListen != "" { args = append(args, "--direct-listen="+c.DirectListen) }
		}
		args = append(args, "--negotiate")
		if contextURL := sessionContextURL(c); contextURL != "" { args = append(args, "--url="+contextURL) }
		if c.MaxPacketSize != 0 {
			args = append(args, fmt.Sprintf("--max-packet-size=%d", c.MaxPacketSize))
		}
		args = append(args, "--cookie-store="+filepath.Join(env("OPENFLUX_STATE_DIR", "/var/lib/openflux-deploy"), "cookies-"+c.ID+".json"))
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

func needsNamedConfig(c Connection) bool {
	seen := make(map[string]bool, len(c.Transports))
	for _, link := range c.Transports {
		if seen[link.Type] { return true }
		seen[link.Type] = true
	}
	return false
}

// OpenFlux derives the AES transport context from --url even in multi-carrier
// mode. The first Yandex document is the mobile client's default context.
func sessionContextURL(c Connection) string {
	if c.SessionContextURL != "" { return c.SessionContextURL }
	for _, link := range c.Transports {
		if link.Type == "yandex" && link.URL != "" { return link.URL }
	}
	for _, link := range c.Transports {
		if link.URL != "" { return link.URL }
	}
	return ""
}

func managedKeyPath(id string) string {
	return filepath.Join(env("OPENFLUX_STATE_DIR", "/var/lib/openflux-deploy"), "keys", id+".key")
}

func cookieStorePath(id string) string {
	return filepath.Join(env("OPENFLUX_STATE_DIR", "/var/lib/openflux-deploy"), "cookies-"+id+".json")
}

// Move an existing single-transport jar from the container working directory
// into the persistent state volume without altering any other document's jar.
func migrateLegacyCookieStore(c Connection) error {
	if c.ID == "" || c.URL == "" || c.Transport == "direct" || len(c.Transports) != 0 { return nil }
	path := cookieStorePath(c.ID)
	if _, err := os.Stat(path); err == nil { return nil } else if !os.IsNotExist(err) { return err }
	raw, err := os.ReadFile("cookies-"+c.Transport+".json")
	if os.IsNotExist(err) { return nil }
	if err != nil { return err }
	var jars map[string]map[string]string
	if err := json.Unmarshal(raw, &jars); err != nil { return err }
	jar := jars[c.URL]
	if len(jar) == 0 { return nil }
	data, err := json.Marshal(map[string]map[string]string{c.URL: jar})
	if err != nil { return err }
	return writePrivateFile(path, data)
}

func sessionConfigPath(id string) string {
	return filepath.Join(env("OPENFLUX_STATE_DIR", "/var/lib/openflux-deploy"), "sessions", id+".conf")
}

func writePrivateFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil { return err }
	tmp, err := os.CreateTemp(filepath.Dir(path), ".openflux-*")
	if err != nil { return err }
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0600); err != nil { tmp.Close(); return err }
	if _, err := tmp.Write(data); err != nil { tmp.Close(); return err }
	if err := tmp.Close(); err != nil { return err }
	return os.Rename(tmp.Name(), path)
}

func prepareKey(c *Connection, previous *Connection) (bool, error) {
	if c.EncryptionKeyFile != "" && !filepath.IsAbs(c.EncryptionKeyFile) {
		if len(c.EncryptionKeyFile) > 4096 || strings.ContainsAny(c.EncryptionKeyFile, "\r\n") { return false, fmt.Errorf("invalid encryption key") }
		path := managedKeyPath(c.ID)
		if err := writePrivateFile(path, []byte(c.EncryptionKeyFile+"\n")); err != nil { return false, err }
		c.EncryptionKeyFile = path
		return true, nil
	}
	if !c.Enabled || (c.Transport != "direct" && !c.Negotiate && len(c.Transports) == 0) || c.EncryptionKeyFile != "" { return false, nil }
	if previous != nil && previous.EncryptionKeyFile != "" {
		c.EncryptionKeyFile = previous.EncryptionKeyFile
		return false, nil
	}
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil { return false, err }
	path := managedKeyPath(c.ID)
	if err := writePrivateFile(path, []byte(hex.EncodeToString(secret[:])+"\n")); err != nil { return false, err }
	c.EncryptionKeyFile = path
	return true, nil
}

func (m *Manager) connectionKey(id string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	i, ok := m.findLocked(id)
	if !ok { return "", os.ErrNotExist }
	if m.connections[i].EncryptionKeyFile != managedKeyPath(id) { return "", fmt.Errorf("key is not managed by the panel") }
	raw, err := os.ReadFile(managedKeyPath(id))
	if err != nil { return "", err }
	return strings.TrimSpace(string(raw)), nil
}

func writeSessionConfig(c Connection) error {
	var b strings.Builder
	counts := map[string]int{}
	for _, link := range c.Transports {
		counts[link.Type]++
		name := link.Type
		if counts[link.Type] > 1 { name = fmt.Sprintf("%s-%d", link.Type, counts[link.Type]) }
		fmt.Fprintf(&b, "[Transport \"%s\"]\nType = %s\nPriority = %d\n", name, link.Type, link.Priority)
		if link.Type == "direct" { fmt.Fprintf(&b, "Listen = %s\n", c.DirectListen) } else { fmt.Fprintf(&b, "URL = %s\n", link.URL) }
		b.WriteByte('\n')
	}
	return writePrivateFile(sessionConfigPath(c.ID), []byte(b.String()))
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
	if usesDirect(c.Config) {
		if !dockerDirectReady() {
			return fmt.Errorf("Docker ещё не подготовлен для Direct: один раз повторно запустите установщик")
		}
		if c.DirectListen == "" {
			return fmt.Errorf("порт Direct не назначен")
		}
	}
	if len(c.Transports) > 0 {
		help, err := exec.Command(m.binaryPath, "--help").CombinedOutput()
		requiredFlag := "--transports="
		if needsNamedConfig(c) { requiredFlag = "--config=" }
		if err != nil || !strings.Contains(string(help), requiredFlag) {
			return fmt.Errorf("this server binary does not support authenticated multi-transport sessions; update the server first")
		}
		if needsNamedConfig(c) { if err := writeSessionConfig(c); err != nil { return err } }
	} else if c.Negotiate {
		help, err := exec.Command(m.binaryPath, "--help").CombinedOutput()
		if err != nil || !strings.Contains(string(help), "--negotiate") {
			return fmt.Errorf("this server binary does not support authenticated negotiation; update the server first")
		}
	}
	if err := migrateLegacyCookieStore(c); err != nil {
		m.logLocked(id, "[panel] cookie migration skipped: "+err.Error())
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
	p.clientCode, p.expectCupsCode, p.yandexAuthRequired = "", false, false
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
	if strings.Contains(line, "[YDOCS] SmartCaptcha detected") || strings.Contains(line, "[YDOCS] fetchDocInfo needs external help") {
		p.yandexAuthRequired = true
	} else if strings.Contains(line, "[YDOCS] WebSocket connected") {
		p.yandexAuthRequired = false
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
	if err := m.prepareDirectLocked("", &c); err != nil {
		return Connection{}, err
	}
	generated, err := prepareKey(&c, nil)
	if err != nil { return Connection{}, err }
	next := append(append([]Connection(nil), m.connections...), c)
	if err := writeJSONFile(m.connectionsPath, next); err != nil {
		if generated { _ = os.Remove(managedKeyPath(c.ID)) }
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
	previous := m.connections[i]
	c.ID, c.OwnerID, c.AutoUpdate = id, previous.OwnerID, false
	if err := m.prepareDirectLocked(id, &c); err != nil {
		return err
	}
	generated, err := prepareKey(&c, &previous)
	if err != nil { return err }
	next := append([]Connection(nil), m.connections...)
	next[i] = c
	if err := writeJSONFile(m.connectionsPath, next); err != nil {
		if generated { _ = os.Remove(managedKeyPath(id)) }
		return err
	}
	m.connections = next
	return m.restartLocked(id)
}

func (m *Manager) validateDirectPortLocked(id, listen string) error {
	if listen == "" {
		return nil
	}
	_, port, _ := net.SplitHostPort(listen) // validateConfig checked the address.
	for _, other := range m.connections {
		if other.ID == id || other.DirectListen == "" {
			continue
		}
		_, occupied, _ := net.SplitHostPort(other.DirectListen)
		if port == occupied {
			return fmt.Errorf("direct port %s is already used by connection %s", port, other.Name)
		}
	}
	return nil
}

func (m *Manager) prepareDirectLocked(id string, c *Connection) error {
	if !usesDirect(c.Config) {
		return nil
	}
	if c.Enabled && !dockerDirectReady() {
		return fmt.Errorf("для Direct требуется однократное обновление Docker; скопируйте команду из панели")
	}
	if c.DirectListen == "" {
		if i, ok := m.findLocked(id); ok && m.connections[i].DirectListen != "" {
			c.DirectListen = m.connections[i].DirectListen
		} else {
			for port := 39000; port <= 39015; port++ {
				candidate := fmt.Sprintf(":%d", port)
				if m.validateDirectPortLocked(id, candidate) == nil {
					listener, err := net.Listen("tcp", candidate)
					if err == nil {
						listener.Close()
						c.DirectListen = candidate
						break
					}
				}
			}
			if c.DirectListen == "" {
				return fmt.Errorf("all Direct ports 39000–39015 are in use")
			}
		}
	}
	return m.validateDirectPortLocked(id, c.DirectListen)
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
		v := ConnectionView{Connection: c, Logs: []string{}, KeyManaged: c.EncryptionKeyFile != "" && c.EncryptionKeyFile == managedKeyPath(c.ID)}
		if len(c.Transports) > 0 { v.SessionContext = sessionContextURL(c) }
		if p := m.processes[c.ID]; p != nil {
			v.Restarts, v.LastError, v.Logs, v.ClientCode, v.YandexAuthRequired = p.restarts, p.lastError, append([]string{}, p.logs...), p.clientCode, p.yandexAuthRequired
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
	return revisionFromFile(m.versionPath)
}

func (m *Manager) panelRevision() string {
	return revisionFromFile(m.panelRevisionPath)
}

func revisionFromFile(path string) string {
	if path == "" {
		return "unknown"
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(raw))
}

func (m *Manager) state(user User) map[string]any {
	state := map[string]any{"me": UserView{user.ID, user.Username, user.Role}, "connections": m.connectionViews(user), "upstream_version": m.version(), "panel_version": panelVersion, "panel_revision": m.panelRevision(), "direct_ports_ready": dockerDirectReady()}
	if user.Role == "admin" {
		m.mu.Lock()
		state["traffic"] = append([]TrafficPoint{}, m.traffic...)
		state["auto_update"] = m.autoUpdate
		state["updating"] = m.updating
		state["update_error"] = m.updateError
		state["server_action"] = m.serverAction
		state["updating_panel"] = m.updatingPanel
		state["panel_update_error"] = m.panelUpdateError
		state["panel_action"] = m.panelAction
		state["checking_versions"] = m.checkingVersions
		if !m.lastVersionCheck.IsZero() {
			state["last_version_check"] = m.lastVersionCheck.Format(time.RFC3339)
		}
		state["latest_upstream"] = m.latestUpstream
		state["latest_panel"] = m.latestPanel
		state["version_check_error"] = m.versionCheckError
		m.mu.Unlock()
		state["rollback_capable"] = m.rollbackCapable
		previousServer, previousPanel := revisionFromFile(m.versionPath+".rollback"), revisionFromFile(m.panelRevisionPath+".rollback")
		state["previous_server_revision"] = previousServer
		state["previous_panel_revision"] = previousPanel
		state["server_rollback_available"] = m.rollbackCapable && regularFile(m.binaryPath+".rollback") && regularFile(m.versionPath+".rollback") && previousServer != m.version()
		state["panel_rollback_available"] = m.rollbackCapable && regularFile(filepath.Join(filepath.Dir(m.panelRevisionPath), "bin/openflux-panel.rollback")) && regularFile(m.panelRevisionPath+".rollback") && previousPanel != m.panelRevision()
		state["panel_update_log"] = tailFile(m.panelUpdateLogPath, 8192)
		state["server_update_log"] = tailFile(m.serverUpdateLogPath, 8192)
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
	m.serverAction = "update"
	m.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(m.serverUpdateLogPath), 0700); err != nil {
		m.mu.Lock()
		m.updating, m.updateError = false, err.Error()
		m.mu.Unlock()
		return err
	}
	logFile, err := os.OpenFile(m.serverUpdateLogPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		m.mu.Lock()
		m.updating, m.updateError = false, err.Error()
		m.mu.Unlock()
		return err
	}
	go func() {
		cmd := exec.Command(m.updatePath, "--force")
		cmd.Stdout, cmd.Stderr = logFile, logFile
		err := cmd.Run()
		logFile.Close()
		m.mu.Lock()
		if err != nil {
			m.updateError = fmt.Sprintf("%v: %s", err, strings.TrimSpace(tailFile(m.serverUpdateLogPath, 4096)))
		}
		m.updating = false
		m.mu.Unlock()
	}()
	return nil
}

func (m *Manager) updatePanel() error { return m.runPanelScript("--force", "update") }

func (m *Manager) runPanelScript(argument, action string) error {
	m.mu.Lock()
	if m.updating || m.updatingPanel {
		m.mu.Unlock()
		return fmt.Errorf("update already running")
	}
	m.updatingPanel = true
	m.panelUpdateError = ""
	m.panelAction = action
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
	cmd := exec.Command(m.panelUpdatePath, argument)
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
