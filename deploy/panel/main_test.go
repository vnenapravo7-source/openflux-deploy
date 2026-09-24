package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func TestValidateConfig(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		ok   bool
	}{
		{"disabled defaults", defaults(), true},
		{"yandex needs url", Config{Enabled: true, Transport: "yandex", Mode: "l3", Codec: "batched"}, false},
		{"yandex url", Config{Enabled: true, Transport: "yandex", URL: "https://docs.yandex.ru/docs/view", Mode: "l3", Codec: "batched"}, true},
		{"board url", Config{Enabled: true, Transport: "boards", URL: "https://boards.yandex.ru/guest/?hash=example", Mode: "l4", Codec: "batched"}, true},
		{"board missing hash", Config{Enabled: true, Transport: "boards", URL: "https://boards.yandex.ru/guest/", Mode: "l4", Codec: "batched"}, false},
		{"board wrong host", Config{Enabled: true, Transport: "boards", URL: "https://example.com/guest/?hash=example", Mode: "l4", Codec: "batched"}, false},
		{"board insecure url", Config{Enabled: true, Transport: "boards", URL: "http://boards.yandex.ru/guest/?hash=example", Mode: "l4", Codec: "batched"}, false},
		{"cups no url", Config{Enabled: true, Transport: "cupsonline", Mode: "l4", Codec: "legacy"}, true},
		{"bad scheme", Config{Enabled: true, Transport: "mailru", URL: "file:///etc/passwd", Mode: "l3", Codec: "batched"}, false},
		{"unknown transport", Config{Transport: "other", Mode: "l3", Codec: "batched"}, false},
		{"authenticated session", Config{Enabled: true, Transport: "yandex", Mode: "l4", Codec: "batched", EncryptionKeyFile: "/etc/openflux-deploy/key", Transports: []TransportLink{{Type: "yandex", URL: "https://docs.yandex.ru/example", Priority: 50}, {Type: "direct", Priority: 100}}, DirectListen: ":39000", MaxPacketSize: 1500}, true},
		{"session without key", Config{Transport: "yandex", Mode: "l4", Codec: "batched", Transports: []TransportLink{{Type: "direct", Priority: 100}}, DirectListen: ":39000"}, false},
		{"session legacy codec", Config{Transport: "yandex", Mode: "l4", Codec: "legacy", EncryptionKeyFile: "/key", Transports: []TransportLink{{Type: "direct", Priority: 100}}, DirectListen: ":39000"}, false},
		{"session duplicate transport", Config{Transport: "yandex", Mode: "l4", Codec: "batched", EncryptionKeyFile: "/key", Transports: []TransportLink{{Type: "direct", Priority: 100}, {Type: "direct", Priority: 50}}, DirectListen: ":39000"}, false},
		{"session invalid packet size", Config{Transport: "yandex", Mode: "l4", Codec: "batched", EncryptionKeyFile: "/key", Transports: []TransportLink{{Type: "direct", Priority: 100}}, DirectListen: ":39000", MaxPacketSize: 1000}, false},
		{"session direct automatic listener", Config{Transport: "yandex", Mode: "l4", Codec: "batched", EncryptionKeyFile: "/key", Transports: []TransportLink{{Type: "direct", Priority: 100}}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateConfig(tc.cfg)
			if (err == nil) != tc.ok {
				t.Fatalf("validateConfig() error = %v, want ok=%v", err, tc.ok)
			}
		})
	}
}

func TestDirectPortAssignedWithoutCollision(t *testing.T) {
	t.Setenv("OPENFLUX_DIRECT_PORTS_PUBLISHED", "1")
	m := &Manager{connections: []Connection{{ID: "existing", Name: "First", Config: Config{DirectListen: ":39000"}}}}
	c := Connection{Config: Config{Transports: []TransportLink{{Type: "direct", Priority: 100}}}}
	if err := m.prepareDirectLocked("", &c); err != nil {
		t.Fatal(err)
	}
	if c.DirectListen == "" || c.DirectListen == ":39000" {
		t.Fatalf("invalid automatic Direct listener: %q", c.DirectListen)
	}
	if err := m.validateDirectPortLocked("", ":39000"); err == nil {
		t.Fatal("duplicate Direct port accepted")
	}
}

func TestBoardConnectionArgs(t *testing.T) {
	c := Connection{Config: Config{Transport: "boards", URL: "https://boards.yandex.ru/guest/?hash=example", Mode: "l4", Codec: "batched"}}
	args := strings.Join(connectionArgs(c), " ")
	if !strings.Contains(args, "--transport=boards") || !strings.Contains(args, "--url="+c.URL) {
		t.Fatalf("board connection arguments: %s", args)
	}
}

func TestAuthenticatedConnectionArgs(t *testing.T) {
	c := Connection{ID: "example", Config: Config{Transport: "yandex", Mode: "l4", Codec: "batched", EncryptionKeyFile: "/etc/openflux-deploy/key", Transports: []TransportLink{{Type: "direct", Priority: 100}, {Type: "yandex", URL: "https://docs.yandex.ru/example", Priority: 50}}, DirectListen: ":39000", MaxPacketSize: 1500}}
	args := strings.Join(connectionArgs(c), " ")
	for _, expected := range []string{"--transports=direct:100,yandex:50", "--negotiate", "--direct-listen=:39000", "--yandex-url=https://docs.yandex.ru/example", "--max-packet-size=1500", "--cookie-store="} {
		if !strings.Contains(args, expected) {
			t.Fatalf("missing %s in %s", expected, args)
		}
	}
	if strings.Contains(args, "--url=") {
		t.Fatalf("session must not set legacy URL (changes handshake context): %s", args)
	}
}

func TestTailFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "panel-update.log")
	if got := tailFile(path, 4); got != "" {
		t.Fatalf("missing log: %q", got)
	}
	if err := os.WriteFile(path, []byte("123456789"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := tailFile(path, 4); got != "6789" {
		t.Fatalf("log tail: %q", got)
	}
}

func TestUsernameChangeRequiresPasswordAndIsUnique(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("current-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	owner := User{ID: "owner", Username: "alice", Role: "user", PasswordHash: string(hash)}
	other := User{ID: "other", Username: "bob", Role: "user", PasswordHash: string(hash)}
	store := &UserStore{path: filepath.Join(t.TempDir(), "users.json"), users: []User{owner, other}}
	s := &server{users: store, sessions: map[string]session{"valid": {userID: owner.ID, expires: time.Now().Add(time.Hour)}}}
	handler := s.auth(http.HandlerFunc(s.api))
	change := func(username, password string) int {
		body, _ := json.Marshal(map[string]string{"username": username, "current_password": password})
		req := httptest.NewRequest(http.MethodPut, "/api/users/owner/username", strings.NewReader(string(body)))
		req.AddCookie(&http.Cookie{Name: "of_session", Value: "valid"})
		req.Header.Set("X-OpenFlux-Action", "1")
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response.Code
	}
	if got := change("alice2", "wrong"); got != http.StatusForbidden {
		t.Fatalf("wrong current password: %d", got)
	}
	if got := change("BOB", "current-password"); got != http.StatusBadRequest {
		t.Fatalf("duplicate username: %d", got)
	}
	if got := change("alice2", "current-password"); got != http.StatusOK {
		t.Fatalf("valid rename: %d", got)
	}
	if u, ok := store.get("owner"); !ok || u.Username != "alice2" {
		t.Fatalf("renamed user: %+v", u)
	}
	if len(s.sessions) != 0 {
		t.Fatal("old session survived username change")
	}
}

func TestPasswordChangeInvalidatesSessions(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("current-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	owner := User{ID: "owner", Username: "alice", Role: "user", PasswordHash: string(hash)}
	store := &UserStore{path: filepath.Join(t.TempDir(), "users.json"), users: []User{owner}}
	s := &server{users: store, sessions: map[string]session{"valid": {userID: owner.ID, expires: time.Now().Add(time.Hour)}}}
	handler := s.auth(http.HandlerFunc(s.api))
	change := func(current string) int {
		body, _ := json.Marshal(map[string]string{"password": "a", "current_password": current})
		req := httptest.NewRequest(http.MethodPut, "/api/users/owner/password", strings.NewReader(string(body)))
		req.AddCookie(&http.Cookie{Name: "of_session", Value: "valid"})
		req.Header.Set("X-OpenFlux-Action", "1")
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response.Code
	}
	if got := change("wrong"); got != http.StatusForbidden {
		t.Fatalf("wrong current password: %d", got)
	}
	if got := change("current-password"); got != http.StatusOK {
		t.Fatalf("valid password change: %d", got)
	}
	if len(s.sessions) != 0 {
		t.Fatal("old session survived password change")
	}
	if _, ok := store.authenticate("alice", "a"); !ok {
		t.Fatal("new password not accepted")
	}
	if _, ok := store.authenticate("alice", "current-password"); ok {
		t.Fatal("old password still accepted")
	}
}

func TestAdminCanResetOtherUsersPassword(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("old-long-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	admin := User{ID: "admin", Username: "admin", Role: "admin", PasswordHash: string(hash)}
	target := User{ID: "target", Username: "member", Role: "user", PasswordHash: string(hash)}
	store := &UserStore{path: filepath.Join(t.TempDir(), "users.json"), users: []User{admin, target}}
	s := &server{users: store, sessions: map[string]session{"admin-session": {userID: admin.ID, expires: time.Now().Add(time.Hour)}, "target-session": {userID: target.ID, expires: time.Now().Add(time.Hour)}}}
	handler := s.auth(http.HandlerFunc(s.api))
	req := httptest.NewRequest(http.MethodPut, "/api/users/target/password", strings.NewReader(`{"password":"b"}`))
	req.AddCookie(&http.Cookie{Name: "of_session", Value: "admin-session"})
	req.Header.Set("X-OpenFlux-Action", "1")
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("admin reset: %d %s", response.Code, response.Body.String())
	}
	if len(s.sessions) != 1 {
		t.Fatalf("unexpected sessions after reset: %v", s.sessions)
	}
	if _, ok := store.authenticate("member", "b"); !ok {
		t.Fatal("replacement password not accepted")
	}
}

func TestPasswordHasNoMinimumLength(t *testing.T) {
	if _, err := hashPassword(""); err == nil {
		t.Fatal("empty password accepted")
	}
	for _, password := range []string{"x", strings.Repeat("Ж", 100)} {
		hash, err := hashPassword(password)
		if err != nil {
			t.Fatalf("password length %d: %v", len(password), err)
		}
		if !passwordMatches(hash, password) || passwordMatches(hash, password+"x") {
			t.Fatalf("password check failed for length %d", len(password))
		}
	}
	store, err := loadUsers(filepath.Join(t.TempDir(), "users.json"), "admin", "x")
	if err != nil {
		t.Fatalf("one-character bootstrap password: %v", err)
	}
	if _, ok := store.authenticate("admin", "x"); !ok {
		t.Fatal("one-character bootstrap password not accepted")
	}
	shortHash, err := hashPassword("x")
	if err != nil {
		t.Fatal(err)
	}
	createStore := &UserStore{path: filepath.Join(t.TempDir(), "users.json"), users: []User{{ID: "admin", Username: "admin", Role: "admin", PasswordHash: shortHash}}}
	if _, err := createStore.add("member", "x", "user"); err != nil {
		t.Fatalf("one-character user password: %v", err)
	}
}

func TestUsersBootstrapAndPasswordHash(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.json")
	store, err := loadUsers(path, "admin", "long-test-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := store.authenticate("admin", "long-test-password"); !ok {
		t.Fatal("admin cannot log in")
	}
	if _, ok := store.authenticate("admin", "wrong-password"); ok {
		t.Fatal("wrong password accepted")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "long-test-password") {
		t.Fatal("plaintext password was persisted")
	}
}

func TestConnectionIsolationAndCupsCode(t *testing.T) {
	owner, other := User{ID: "owner", Role: "user"}, User{ID: "other", Role: "user"}
	id := "first"
	m := &Manager{connections: []Connection{{ID: id, OwnerID: owner.ID, Name: "Cups", Config: Config{Transport: "cupsonline", Mode: "l4", Codec: "batched"}}, {ID: "second", OwnerID: other.ID, Name: "Other", Config: defaults()}}, processes: map[string]*processState{id: {}, "second": {}}}
	code := base64.RawURLEncoding.EncodeToString([]byte(`["12345678-1234-1234-1234-123456789abc"]`))
	m.logLocked(id, "=== COPY THIS TO CLIENT ===")
	m.logLocked(id, code)
	views := m.connectionViews(owner)
	if len(views) != 1 || views[0].ID != id || views[0].ClientCode != code {
		t.Fatalf("owner view: %+v", views)
	}
	if got := m.connectionViews(other); len(got) != 1 || got[0].ClientCode != "" {
		t.Fatalf("other view: %+v", got)
	}
	if got := m.connectionViews(User{Role: "admin"}); len(got) != 2 {
		t.Fatalf("admin sees %d connections", len(got))
	}
}

func TestStateRequiresSessionAndHidesOtherConnections(t *testing.T) {
	owner, other := User{ID: "owner", Username: "owner", Role: "user"}, User{ID: "other", Username: "other", Role: "user"}
	m := &Manager{connections: []Connection{{ID: "one", OwnerID: owner.ID, Name: "private", Config: defaults()}, {ID: "two", OwnerID: other.ID, Name: "hidden", Config: defaults()}}, processes: map[string]*processState{"one": {}, "two": {}}, nodes: []Node{}, traffic: []TrafficPoint{}}
	s := &server{mgr: m, users: &UserStore{users: []User{owner, other}}, sessions: map[string]session{"valid": {userID: owner.ID, expires: time.Now().Add(time.Hour)}}, attempts: map[string]loginAttempt{}}
	handler := s.auth(http.HandlerFunc(s.api))
	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/api/state", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status %d", unauthorized.Code)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/state", nil)
	request.AddCookie(&http.Cookie{Name: "of_session", Value: "valid"})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status %d", response.Code)
	}
	var result struct {
		Connections []ConnectionView `json:"connections"`
		Nodes       json.RawMessage  `json:"nodes"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Connections) != 1 || result.Connections[0].ID != "one" || result.Nodes != nil {
		t.Fatalf("unfiltered response: %s", response.Body.String())
	}
	otherRestart := httptest.NewRequest(http.MethodPost, "/api/connections/two/restart", nil)
	otherRestart.AddCookie(&http.Cookie{Name: "of_session", Value: "valid"})
	otherRestart.Header.Set("X-OpenFlux-Action", "1")
	denied := httptest.NewRecorder()
	handler.ServeHTTP(denied, otherRestart)
	if denied.Code != http.StatusNotFound {
		t.Fatalf("other user's connection action: %d", denied.Code)
	}
	usersRequest := httptest.NewRequest(http.MethodGet, "/api/users", nil)
	usersRequest.AddCookie(&http.Cookie{Name: "of_session", Value: "valid"})
	usersResponse := httptest.NewRecorder()
	handler.ServeHTTP(usersResponse, usersRequest)
	if usersResponse.Code != http.StatusForbidden {
		t.Fatalf("user listing: %d", usersResponse.Code)
	}
	otherRename := httptest.NewRequest(http.MethodPut, "/api/users/other/username", strings.NewReader(`{"username":"stolen"}`))
	otherRename.AddCookie(&http.Cookie{Name: "of_session", Value: "valid"})
	otherRename.Header.Set("X-OpenFlux-Action", "1")
	otherRename.Header.Set("Content-Type", "application/json")
	renameResponse := httptest.NewRecorder()
	handler.ServeHTTP(renameResponse, otherRename)
	if renameResponse.Code != http.StatusForbidden {
		t.Fatalf("renaming other user allowed: %d", renameResponse.Code)
	}
	panelUpdate := httptest.NewRequest(http.MethodPost, "/api/update-panel", nil)
	panelUpdate.AddCookie(&http.Cookie{Name: "of_session", Value: "valid"})
	panelUpdate.Header.Set("X-OpenFlux-Action", "1")
	panelResponse := httptest.NewRecorder()
	handler.ServeHTTP(panelResponse, panelUpdate)
	if panelResponse.Code != http.StatusForbidden {
		t.Fatalf("panel update allowed for ordinary user: %d", panelResponse.Code)
	}
	for _, path := range []string{"/api/rollback-server", "/api/rollback-panel", "/api/check-updates"} {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		req.AddCookie(&http.Cookie{Name: "of_session", Value: "valid"})
		req.Header.Set("X-OpenFlux-Action", "1")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		if response.Code != http.StatusForbidden {
			t.Fatalf("ordinary user allowed %s: %d", path, response.Code)
		}
	}
}

func TestRollbackAvailabilityRequiresBinaryAndRevision(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin", "openflux")
	panel := filepath.Join(dir, "bin", "openflux-panel")
	version := filepath.Join(dir, "upstream-version")
	panelVersion := filepath.Join(dir, "panel-revision")
	if err := os.MkdirAll(filepath.Dir(bin), 0700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{bin + ".rollback", panel + ".rollback", version + ".rollback", panelVersion + ".rollback"} {
		if err := os.WriteFile(path, []byte("previous"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	m := &Manager{binaryPath: bin, versionPath: version, panelRevisionPath: panelVersion, rollbackCapable: true, processes: map[string]*processState{}}
	state := m.state(User{Role: "admin"})
	if state["server_rollback_available"] != true || state["panel_rollback_available"] != true {
		t.Fatalf("backups not detected: %+v", state)
	}
	if err := os.Remove(version + ".rollback"); err != nil {
		t.Fatal(err)
	}
	state = m.state(User{Role: "admin"})
	if state["server_rollback_available"] != false || state["panel_rollback_available"] != true {
		t.Fatalf("incomplete backup not detected: %+v", state)
	}
}

func TestRollbackRequiresUpdatedInstaller(t *testing.T) {
	m := &Manager{}
	if err := m.rollbackServer(); err == nil {
		t.Fatal("server rollback accepted with an old installer")
	}
	if err := m.rollbackPanel(); err == nil {
		t.Fatal("panel rollback accepted with an old installer")
	}
}

func TestLegacyConnectionMigration(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "config.json")
	connections := filepath.Join(dir, "connections.json")
	old := Config{Transport: "yandex", URL: "https://docs.yandex.ru/example", Mode: "l4", Codec: "batched", AutoUpdate: true}
	if err := writeJSONFile(legacy, old); err != nil {
		t.Fatal(err)
	}
	m, err := NewManager(legacy, connections, filepath.Join(dir, "nodes.json"), "missing-binary", filepath.Join(dir, "version"), "missing-updater", "admin-id")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.connections) != 1 || m.connections[0].OwnerID != "admin-id" || m.connections[0].URL != old.URL {
		t.Fatalf("migration: %+v", m.connections)
	}
}

func TestValidateNode(t *testing.T) {
	n := Node{Name: "edge-1", BaseURL: "https://node.example:8088", Token: strings.Repeat("a", 32), TLSSHA256: strings.Repeat("ab", 32)}
	if err := validateNode(n); err != nil {
		t.Fatal(err)
	}
	n.BaseURL = "http://node.example:8088"
	if err := validateNode(n); err == nil {
		t.Fatal("expected HTTPS validation error")
	}
}
