package main

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestInstructionStorePersistsTextAndOrder(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "instructions.json")
	s, err := loadInstructions(path, filepath.Join(dir, "assets"))
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.addText("Первый", "**жирный**")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.addText("Второй", "- пункт")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.move(b.ID, -1); err != nil {
		t.Fatal(err)
	}
	if err := s.update(a.ID, "Обновлено", "новый текст"); err != nil {
		t.Fatal(err)
	}
	reloaded, err := loadInstructions(path, s.assetsDir)
	if err != nil {
		t.Fatal(err)
	}
	blocks := reloaded.list()
	if len(blocks) != 2 || blocks[0].ID != b.ID || blocks[1].Title != "Обновлено" || blocks[1].Body != "новый текст" {
		t.Fatalf("stored blocks: %+v", blocks)
	}
	if err := reloaded.delete(b.ID); err != nil {
		t.Fatal(err)
	}
	if len(reloaded.list()) != 1 {
		t.Fatal("deleted block still present")
	}
	if _, err := reloaded.addText("", ""); err == nil {
		t.Fatal("empty text accepted")
	}
}

func TestInstructionMediaTypes(t *testing.T) {
	if _, _, ok := allowedInstructionMedia("image", "image/png", "image/png", "picture.png"); !ok {
		t.Fatal("PNG image rejected")
	}
	if _, _, ok := allowedInstructionMedia("image", "text/html; charset=utf-8", "image/png", "picture.png"); ok {
		t.Fatal("HTML disguised as PNG accepted")
	}
	if _, _, ok := allowedInstructionMedia("video", "application/octet-stream", "video/mp4", "clip.mp4"); !ok {
		t.Fatal("MP4 video rejected")
	}
	if _, _, ok := allowedInstructionMedia("video", "application/octet-stream", "application/octet-stream", "clip.webm"); !ok {
		t.Fatal("WebM video with generic browser MIME rejected")
	}
	if _, _, ok := allowedInstructionMedia("video", "text/html; charset=utf-8", "video/mp4", "clip.mp4"); ok {
		t.Fatal("HTML disguised as video accepted")
	}
}

func TestInstructionsAPIAuthAndUpload(t *testing.T) {
	dir := t.TempDir()
	store, err := loadInstructions(filepath.Join(dir, "instructions.json"), filepath.Join(dir, "assets"))
	if err != nil {
		t.Fatal(err)
	}
	admin := User{ID: "admin", Username: "admin", Role: "admin"}
	member := User{ID: "member", Username: "member", Role: "user"}
	srv := &server{instructions: store, users: &UserStore{users: []User{admin, member}}, sessions: map[string]session{"admin": {userID: admin.ID, expires: time.Now().Add(time.Hour)}, "member": {userID: member.ID, expires: time.Now().Add(time.Hour)}}}
	handler := srv.auth(http.HandlerFunc(srv.api))
	request := func(method, path, session, contentType string, body *bytes.Buffer) *httptest.ResponseRecorder {
		var payload *bytes.Buffer
		if body != nil {
			payload = body
		} else {
			payload = &bytes.Buffer{}
		}
		req := httptest.NewRequest(method, path, payload)
		if session != "" {
			req.AddCookie(&http.Cookie{Name: "of_session", Value: session})
		}
		req.Header.Set("X-OpenFlux-Action", "1")
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response
	}
	textBody := bytes.NewBufferString(`{"title":"Welcome","body":"## Instructions"}`)
	if got := request(http.MethodPost, "/api/instructions", "member", "application/json", textBody); got.Code != http.StatusForbidden {
		t.Fatalf("ordinary user added text: %d", got.Code)
	}
	if got := request(http.MethodPost, "/api/instructions", "admin", "application/json", bytes.NewBufferString(`{"title":"Welcome","body":"## Instructions"}`)); got.Code != http.StatusCreated {
		t.Fatalf("admin create text: %d %s", got.Code, got.Body.String())
	}
	if got := request(http.MethodGet, "/api/instructions", "member", "", nil); got.Code != http.StatusOK || !strings.Contains(got.Body.String(), "Welcome") {
		t.Fatalf("ordinary user cannot read instructions: %d %s", got.Code, got.Body.String())
	}
	var payload bytes.Buffer
	form := multipart.NewWriter(&payload)
	_ = form.WriteField("kind", "image")
	_ = form.WriteField("title", "Screenshot")
	part, err := form.CreateFormFile("file", "screenshot.png")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write([]byte("\x89PNG\r\n\x1a\nimage-data"))
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}
	response := request(http.MethodPost, "/api/instructions/upload", "admin", form.FormDataContentType(), &payload)
	if response.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", response.Code, response.Body.String())
	}
	var block InstructionBlock
	if err := json.Unmarshal(response.Body.Bytes(), &block); err != nil {
		t.Fatal(err)
	}
	if block.Kind != "image" || block.MediaType != "image/png" || !regularFile(filepath.Join(store.assetsDir, block.ID)) {
		t.Fatalf("stored image: %+v", block)
	}
	assetPath := "/api/instructions/assets/" + block.ID
	if got := request(http.MethodGet, assetPath, "", "", nil); got.Code != http.StatusUnauthorized {
		t.Fatalf("public asset access: %d", got.Code)
	}
	if got := request(http.MethodGet, assetPath, "member", "", nil); got.Code != http.StatusOK || !strings.HasPrefix(got.Header().Get("Content-Type"), "image/png") {
		t.Fatalf("authenticated asset access: %d %s", got.Code, got.Header().Get("Content-Type"))
	}
	var replacement bytes.Buffer
	replaceForm := multipart.NewWriter(&replacement)
	_ = replaceForm.WriteField("kind", "image")
	_ = replaceForm.WriteField("title", "New screenshot")
	replacePart, err := replaceForm.CreateFormFile("file", "replacement.png")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = replacePart.Write([]byte("\x89PNG\r\n\x1a\nreplacement"))
	if err := replaceForm.Close(); err != nil {
		t.Fatal(err)
	}
	if got := request(http.MethodPut, "/api/instructions/"+block.ID+"/asset", "admin", replaceForm.FormDataContentType(), &replacement); got.Code != http.StatusOK {
		t.Fatalf("replace asset: %d %s", got.Code, got.Body.String())
	}
	if blocks := store.list(); len(blocks) != 2 || blocks[1].ID != block.ID || blocks[1].Title != "New screenshot" {
		t.Fatalf("replacement changed block order or id: %+v", blocks)
	}
	if raw, err := os.ReadFile(filepath.Join(store.assetsDir, block.ID)); err != nil || !bytes.Contains(raw, []byte("replacement")) {
		t.Fatalf("replacement file missing: %v", err)
	}
	if got := request(http.MethodDelete, "/api/instructions/"+block.ID, "admin", "", nil); got.Code != http.StatusOK {
		t.Fatalf("delete: %d", got.Code)
	}
	if _, err := os.Stat(filepath.Join(store.assetsDir, block.ID)); !os.IsNotExist(err) {
		t.Fatalf("asset survived deletion: %v", err)
	}
}
