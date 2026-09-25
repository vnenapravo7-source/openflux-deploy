package main

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

const panelVersion = "0.6.7"

//go:embed static/*
var staticFiles embed.FS

type contextUserKey struct{}
type session struct {
	userID  string
	expires time.Time
}
type loginAttempt struct {
	failures int
	since    time.Time
}

type server struct {
	mgr           *Manager
	users         *UserStore
	instructions  *InstructionStore
	nodeToken     string
	secureCookies bool
	mu            sync.Mutex
	sessions      map[string]session
	attempts      map[string]loginAttempt
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func apiError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
func actionAllowed(r *http.Request) bool { return r.Header.Get("X-OpenFlux-Action") == "1" }

func decodeBody(w http.ResponseWriter, r *http.Request, value any) error {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		return fmt.Errorf("JSON body required")
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024))
	dec.DisallowUnknownFields()
	if err := dec.Decode(value); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return fmt.Errorf("only one JSON value is allowed")
	}
	return nil
}

func (s *server) login(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !actionAllowed(r) {
		apiError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeBody(w, r, &input); err != nil {
		apiError(w, http.StatusBadRequest, err.Error())
		return
	}
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		ip = r.RemoteAddr
	}
	s.mu.Lock()
	a := s.attempts[ip]
	blocked := a.failures >= 10 && time.Since(a.since) < 5*time.Minute
	s.mu.Unlock()
	if blocked {
		apiError(w, http.StatusTooManyRequests, "too many login attempts; try later")
		return
	}
	u, ok := s.users.authenticate(input.Username, input.Password)
	if !ok {
		s.mu.Lock()
		if time.Since(a.since) > 5*time.Minute {
			a = loginAttempt{since: time.Now()}
		}
		a.failures++
		s.attempts[ip] = a
		s.mu.Unlock()
		apiError(w, http.StatusUnauthorized, "invalid username or password")
		return
	}
	token, err := newID()
	if err != nil {
		apiError(w, http.StatusInternalServerError, "session creation failed")
		return
	}
	s.mu.Lock()
	delete(s.attempts, ip)
	s.sessions[token] = session{userID: u.ID, expires: time.Now().Add(24 * time.Hour)}
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "of_session", Value: token, Path: "/", HttpOnly: true, Secure: s.secureCookies, SameSite: http.SameSiteStrictMode, MaxAge: 86400})
	writeJSON(w, http.StatusOK, map[string]any{"me": UserView{u.ID, u.Username, u.Role}})
}

func (s *server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" || r.URL.Path == "/api/login" || !strings.HasPrefix(r.URL.Path, "/api/") && !strings.HasPrefix(r.URL.Path, "/node/v1/") {
			next.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/node/v1/") {
			provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if s.nodeToken == "" || subtle.ConstantTimeCompare([]byte(provided), []byte(s.nodeToken)) != 1 {
				apiError(w, http.StatusUnauthorized, "invalid node token")
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		cookie, err := r.Cookie("of_session")
		if err != nil {
			apiError(w, http.StatusUnauthorized, "login required")
			return
		}
		s.mu.Lock()
		sess, ok := s.sessions[cookie.Value]
		s.mu.Unlock()
		if !ok || time.Now().After(sess.expires) {
			apiError(w, http.StatusUnauthorized, "login required")
			return
		}
		u, ok := s.users.get(sess.userID)
		if !ok {
			apiError(w, http.StatusUnauthorized, "account unavailable")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), contextUserKey{}, u)))
	})
}

func currentUser(r *http.Request) User { u, _ := r.Context().Value(contextUserKey{}).(User); return u }
func requireAdmin(w http.ResponseWriter, u User) bool {
	if u.Role != "admin" {
		apiError(w, http.StatusForbidden, "administrator required")
		return false
	}
	return true
}
func requireAction(w http.ResponseWriter, r *http.Request) bool {
	if !actionAllowed(r) {
		apiError(w, http.StatusForbidden, "action header required")
		return false
	}
	return true
}

func (s *server) invalidateUser(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for token, session := range s.sessions {
		if session.userID == id {
			delete(s.sessions, token)
		}
	}
}

func (s *server) api(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	path := r.URL.Path
	switch {
	case r.Method == http.MethodGet && path == "/api/state":
		writeJSON(w, http.StatusOK, s.mgr.state(u))
	case strings.HasPrefix(path, "/api/instructions"):
		s.instructionAPI(w, r, u)
	case r.Method == http.MethodPost && path == "/api/logout":
		if !requireAction(w, r) {
			return
		}
		if c, err := r.Cookie("of_session"); err == nil {
			s.mu.Lock()
			delete(s.sessions, c.Value)
			s.mu.Unlock()
		}
		http.SetCookie(w, &http.Cookie{Name: "of_session", Path: "/", Value: "", MaxAge: -1, HttpOnly: true, Secure: s.secureCookies, SameSite: http.SameSiteStrictMode})
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	case r.Method == http.MethodGet && path == "/api/users":
		if !requireAdmin(w, u) {
			return
		}
		writeJSON(w, http.StatusOK, s.users.list())
	case r.Method == http.MethodPost && path == "/api/users":
		if !requireAdmin(w, u) || !requireAction(w, r) {
			return
		}
		var input struct {
			Username string `json:"username"`
			Password string `json:"password"`
			Role     string `json:"role"`
		}
		if err := decodeBody(w, r, &input); err != nil {
			apiError(w, http.StatusBadRequest, err.Error())
			return
		}
		view, err := s.users.add(input.Username, input.Password, input.Role)
		if err != nil {
			apiError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, view)
	case strings.HasPrefix(path, "/api/users/"):
		id := strings.TrimPrefix(path, "/api/users/")
		if strings.HasSuffix(id, "/username") && r.Method == http.MethodPut {
			id = strings.TrimSuffix(id, "/username")
			if u.ID != id && !requireAdmin(w, u) {
				return
			}
			if !requireAction(w, r) {
				return
			}
			var input struct {
				Username        string `json:"username"`
				CurrentPassword string `json:"current_password"`
			}
			if err := decodeBody(w, r, &input); err != nil {
				apiError(w, http.StatusBadRequest, err.Error())
				return
			}
			if u.ID == id && !s.users.verifyPassword(id, input.CurrentPassword) {
				apiError(w, http.StatusForbidden, "current password is incorrect")
				return
			}
			if err := s.users.changeUsername(id, input.Username); err != nil {
				apiError(w, http.StatusBadRequest, err.Error())
				return
			}
			s.invalidateUser(id)
			writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
			return
		}
		if strings.HasSuffix(id, "/password") && r.Method == http.MethodPut {
			id = strings.TrimSuffix(id, "/password")
			if u.ID != id && !requireAdmin(w, u) {
				return
			}
			if !requireAction(w, r) {
				return
			}
			var input struct {
				Password        string `json:"password"`
				CurrentPassword string `json:"current_password"`
			}
			if err := decodeBody(w, r, &input); err != nil {
				apiError(w, http.StatusBadRequest, err.Error())
				return
			}
			if u.ID == id && !s.users.verifyPassword(id, input.CurrentPassword) {
				apiError(w, http.StatusForbidden, "current password is incorrect")
				return
			}
			if err := s.users.changePassword(id, input.Password); err != nil {
				apiError(w, http.StatusBadRequest, err.Error())
				return
			}
			s.invalidateUser(id)
			writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
			return
		}
		if r.Method == http.MethodDelete {
			if !requireAdmin(w, u) || !requireAction(w, r) {
				return
			}
			if u.ID == id {
				apiError(w, http.StatusBadRequest, "cannot delete your own account")
				return
			}
			if _, ok := s.users.get(id); !ok {
				apiError(w, http.StatusNotFound, "user not found")
				return
			}
			if err := s.mgr.removeUserConnections(id); err != nil {
				apiError(w, http.StatusInternalServerError, err.Error())
				return
			}
			if err := s.users.delete(id); err != nil {
				apiError(w, http.StatusBadRequest, err.Error())
				return
			}
			s.invalidateUser(id)
			writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
			return
		}
		http.NotFound(w, r)
	case r.Method == http.MethodPost && path == "/api/connections":
		if !requireAction(w, r) {
			return
		}
		var c Connection
		if err := decodeBody(w, r, &c); err != nil {
			apiError(w, http.StatusBadRequest, err.Error())
			return
		}
		if u.Role != "admin" {
			c.OwnerID = u.ID
		} else if c.OwnerID == "" {
			c.OwnerID = u.ID
		}
		if _, ok := s.users.get(c.OwnerID); !ok {
			apiError(w, http.StatusBadRequest, "owner not found")
			return
		}
		added, err := s.mgr.addConnection(c)
		if err != nil {
			apiError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, added)
	case strings.HasPrefix(path, "/api/connections/"):
		tail := strings.TrimPrefix(path, "/api/connections/")
		parts := strings.Split(tail, "/")
		if len(parts) > 2 || parts[0] == "" {
			http.NotFound(w, r)
			return
		}
		current, ok := s.mgr.connection(parts[0])
		if !ok {
			apiError(w, http.StatusNotFound, "connection not found")
			return
		}
		if u.Role != "admin" && current.OwnerID != u.ID {
			apiError(w, http.StatusNotFound, "connection not found")
			return
		}
		if !requireAction(w, r) {
			return
		}
		if len(parts) == 2 && parts[1] == "key" && r.Method == http.MethodPost {
			secret, err := s.mgr.connectionKey(parts[0])
			if err != nil {
				apiError(w, http.StatusBadRequest, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]string{"key": secret})
			return
		}
		if len(parts) == 2 && parts[1] == "share" && r.Method == http.MethodPost {
			var input struct {
				Host string `json:"host"`
			}
			if err := decodeBody(w, r, &input); err != nil {
				apiError(w, http.StatusBadRequest, err.Error())
				return
			}
			result, err := s.mgr.connectionShare(parts[0], input.Host)
			if err != nil {
				apiError(w, http.StatusBadRequest, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, result)
			return
		}
		if len(parts) == 2 && parts[1] == "restart" && r.Method == http.MethodPost {
			if err := s.mgr.restart(parts[0]); err != nil {
				apiError(w, http.StatusInternalServerError, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
			return
		}
		if len(parts) == 1 && r.Method == http.MethodPut {
			var c Connection
			if err := decodeBody(w, r, &c); err != nil {
				apiError(w, http.StatusBadRequest, err.Error())
				return
			}
			if err := s.mgr.updateConnection(parts[0], c); err != nil {
				apiError(w, http.StatusBadRequest, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
			return
		}
		if len(parts) == 1 && r.Method == http.MethodDelete {
			if err := s.mgr.deleteConnection(parts[0]); err != nil {
				apiError(w, http.StatusInternalServerError, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
			return
		}
		http.NotFound(w, r)
	case r.Method == http.MethodGet && path == "/api/nodes":
		if !requireAdmin(w, u) {
			return
		}
		writeJSON(w, http.StatusOK, s.mgr.nodeViews())
	case r.Method == http.MethodPost && path == "/api/nodes":
		if !requireAdmin(w, u) || !requireAction(w, r) {
			return
		}
		var n Node
		if err := decodeBody(w, r, &n); err != nil {
			apiError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := s.mgr.addNode(n); err != nil {
			apiError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, map[string]bool{"ok": true})
	case strings.HasPrefix(path, "/api/nodes/"):
		if !requireAdmin(w, u) || !requireAction(w, r) {
			return
		}
		parts := strings.Split(strings.TrimPrefix(path, "/api/nodes/"), "/")
		if len(parts) == 1 && r.Method == http.MethodDelete {
			if err := s.mgr.deleteNode(parts[0]); err != nil {
				apiError(w, http.StatusNotFound, "node not found")
				return
			}
			writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
			return
		}
		if len(parts) == 2 && r.Method == http.MethodPost && (parts[1] == "restart" || parts[1] == "update") {
			for _, n := range s.mgr.nodesSnapshot() {
				if n.ID == parts[0] {
					if err := callNode(n, http.MethodPost, "/node/v1/"+parts[1], nil); err != nil {
						apiError(w, http.StatusBadGateway, err.Error())
						return
					}
					writeJSON(w, http.StatusAccepted, map[string]bool{"ok": true})
					return
				}
			}
			apiError(w, http.StatusNotFound, "node not found")
			return
		}
		http.NotFound(w, r)
	case r.Method == http.MethodPut && path == "/api/settings":
		if !requireAdmin(w, u) || !requireAction(w, r) {
			return
		}
		var input struct {
			AutoUpdate bool `json:"auto_update"`
		}
		if err := decodeBody(w, r, &input); err != nil {
			apiError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := s.mgr.setAutoUpdate(input.AutoUpdate); err != nil {
			apiError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	case r.Method == http.MethodPost && path == "/api/update":
		if !requireAdmin(w, u) || !requireAction(w, r) {
			return
		}
		if err := s.mgr.update(); err != nil {
			apiError(w, http.StatusConflict, err.Error())
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]bool{"ok": true})
	case r.Method == http.MethodPost && path == "/api/rollback-server":
		if !requireAdmin(w, u) || !requireAction(w, r) {
			return
		}
		if err := s.mgr.rollbackServer(); err != nil {
			apiError(w, http.StatusConflict, err.Error())
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]bool{"ok": true})
	case r.Method == http.MethodPost && path == "/api/update-panel":
		if !requireAdmin(w, u) || !requireAction(w, r) {
			return
		}
		if err := s.mgr.updatePanel(); err != nil {
			apiError(w, http.StatusConflict, err.Error())
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]bool{"ok": true})
	case r.Method == http.MethodPost && path == "/api/rollback-panel":
		if !requireAdmin(w, u) || !requireAction(w, r) {
			return
		}
		if err := s.mgr.rollbackPanel(); err != nil {
			apiError(w, http.StatusConflict, err.Error())
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]bool{"ok": true})
	case r.Method == http.MethodPost && path == "/api/check-updates":
		if !requireAdmin(w, u) || !requireAction(w, r) {
			return
		}
		s.mgr.checkVersions(r.URL.Query().Get("force") == "1")
		writeJSON(w, http.StatusAccepted, map[string]bool{"ok": true})
	default:
		http.NotFound(w, r)
	}
}

func (s *server) nodeAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && r.URL.Path == "/node/v1/state" {
		writeJSON(w, http.StatusOK, s.mgr.nodeState())
		return
	}
	if r.Method == http.MethodPost && actionAllowed(r) && r.URL.Path == "/node/v1/restart" {
		s.mgr.restartAll()
		writeJSON(w, http.StatusAccepted, map[string]bool{"ok": true})
		return
	}
	if r.Method == http.MethodPost && actionAllowed(r) && r.URL.Path == "/node/v1/update" {
		if err := s.mgr.update(); err != nil {
			apiError(w, http.StatusConflict, err.Error())
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]bool{"ok": true})
		return
	}
	http.NotFound(w, r)
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Println("OpenFlux panel " + panelVersion)
		return
	}
	userName := env("OPENFLUX_ADMIN_USER", "admin")
	adminPassword := os.Getenv("OPENFLUX_ADMIN_PASSWORD")
	users, err := loadUsers(env("OPENFLUX_USERS", "/etc/openflux-deploy/users.json"), userName, adminPassword)
	if err != nil {
		log.Fatal(err)
	}
	mgr, err := NewManager(
		env("OPENFLUX_CONFIG", "/etc/openflux-deploy/config.json"),
		env("OPENFLUX_CONNECTIONS", "/etc/openflux-deploy/connections.json"),
		env("OPENFLUX_NODES", "/etc/openflux-deploy/nodes.json"),
		env("OPENFLUX_BINARY", "/var/lib/openflux-deploy/bin/openflux"),
		env("OPENFLUX_VERSION_FILE", "/var/lib/openflux-deploy/upstream-version"),
		env("OPENFLUX_UPDATE_SCRIPT", "/usr/local/lib/openflux-deploy/update.sh"), users.adminID())
	if err != nil {
		log.Fatal(err)
	}
	mgr.scheduleBoardRepair()
	instructions, err := loadInstructions(env("OPENFLUX_INSTRUCTIONS", "/etc/openflux-deploy/instructions.json"), env("OPENFLUX_INSTRUCTION_ASSETS", "/var/lib/openflux-deploy/instruction-assets"))
	if err != nil {
		log.Fatal(err)
	}
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	go func() {
		for range hup {
			mgr.restartAll()
		}
	}()
	web, err := fs.Sub(staticFiles, "static")
	if err != nil {
		log.Fatal(err)
	}
	srv := &server{mgr: mgr, users: users, instructions: instructions, nodeToken: os.Getenv("OPENFLUX_NODE_TOKEN"), secureCookies: os.Getenv("OPENFLUX_TLS_CERT") != "" && os.Getenv("OPENFLUX_TLS_KEY") != "", sessions: map[string]session{}, attempts: map[string]loginAttempt{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, http.StatusOK, mgr.health()) })
	mux.HandleFunc("/api/login", srv.login)
	mux.HandleFunc("/api/", srv.api)
	mux.HandleFunc("/node/v1/", srv.nodeAPI)
	staticHandler := http.FileServer(http.FS(web))
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		staticHandler.ServeHTTP(w, r)
	}))
	server := &http.Server{Addr: env("OPENFLUX_LISTEN", ":8088"), Handler: srv.auth(mux), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
	cert, key := os.Getenv("OPENFLUX_TLS_CERT"), os.Getenv("OPENFLUX_TLS_KEY")
	log.Printf("OpenFlux panel %s listening on %s", panelVersion, server.Addr)
	if cert != "" && key != "" {
		err = server.ListenAndServeTLS(cert, key)
	} else {
		err = server.ListenAndServe()
	}
	if !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
