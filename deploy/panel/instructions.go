package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const maxInstructionUpload = 200 << 20

type InstructionBlock struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Title     string `json:"title"`
	Body      string `json:"body,omitempty"`
	FileName  string `json:"file_name,omitempty"`
	MediaType string `json:"media_type,omitempty"`
	Size      int64  `json:"size,omitempty"`
}

type InstructionStore struct {
	mu        sync.Mutex
	path      string
	assetsDir string
	blocks    []InstructionBlock
}

func loadInstructions(path, assetsDir string) (*InstructionStore, error) {
	s := &InstructionStore{path: path, assetsDir: assetsDir, blocks: []InstructionBlock{}}
	raw, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(raw, &s.blocks); err != nil {
			return nil, fmt.Errorf("instructions file: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return s, nil
}

func (s *InstructionStore) list() []InstructionBlock {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]InstructionBlock{}, s.blocks...)
}

func validInstructionTitle(title string) bool { return len([]rune(strings.TrimSpace(title))) <= 160 }

func (s *InstructionStore) addText(title, body string) (InstructionBlock, error) {
	title, body = strings.TrimSpace(title), strings.TrimSpace(body)
	if !validInstructionTitle(title) || body == "" || len(body) > 60*1024 {
		return InstructionBlock{}, fmt.Errorf("text is required (up to 60 KB); title may contain up to 160 characters")
	}
	id, err := newID()
	if err != nil {
		return InstructionBlock{}, err
	}
	block := InstructionBlock{ID: id, Kind: "text", Title: title, Body: body}
	s.mu.Lock()
	defer s.mu.Unlock()
	next := append(append([]InstructionBlock{}, s.blocks...), block)
	if err := writeJSONFile(s.path, next); err != nil {
		return InstructionBlock{}, err
	}
	s.blocks = next
	return block, nil
}

func (s *InstructionStore) update(id, title, body string) error {
	title, body = strings.TrimSpace(title), strings.TrimSpace(body)
	if !validInstructionTitle(title) {
		return fmt.Errorf("title may contain up to 160 characters")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	next := append([]InstructionBlock{}, s.blocks...)
	for i := range next {
		if next[i].ID != id {
			continue
		}
		if next[i].Kind == "text" && (body == "" || len(body) > 60*1024) {
			return fmt.Errorf("text is required (up to 60 KB)")
		}
		next[i].Title = title
		if next[i].Kind == "text" {
			next[i].Body = body
		}
		if err := writeJSONFile(s.path, next); err != nil {
			return err
		}
		s.blocks = next
		return nil
	}
	return os.ErrNotExist
}

func (s *InstructionStore) move(id string, direction int) error {
	if direction != -1 && direction != 1 {
		return fmt.Errorf("direction must be -1 or 1")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	next := append([]InstructionBlock{}, s.blocks...)
	for i := range next {
		if next[i].ID == id {
			j := i + direction
			if j < 0 || j >= len(next) {
				return nil
			}
			next[i], next[j] = next[j], next[i]
			if err := writeJSONFile(s.path, next); err != nil {
				return err
			}
			s.blocks = next
			return nil
		}
	}
	return os.ErrNotExist
}

func (s *InstructionStore) delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, block := range s.blocks {
		if block.ID != id {
			continue
		}
		next := append(append([]InstructionBlock{}, s.blocks[:i]...), s.blocks[i+1:]...)
		if err := writeJSONFile(s.path, next); err != nil {
			return err
		}
		s.blocks = next
		if block.Kind != "text" {
			_ = os.Remove(filepath.Join(s.assetsDir, id))
		}
		return nil
	}
	return os.ErrNotExist
}

func allowedInstructionMedia(kind, detected, declared, name string) (string, int64, bool) {
	switch kind {
	case "image":
		switch detected {
		case "image/jpeg", "image/png", "image/gif", "image/webp":
			return detected, 20 << 20, true
		}
	case "video":
		ext := strings.ToLower(filepath.Ext(name))
		videoTypes := map[string]string{".mp4": "video/mp4", ".webm": "video/webm", ".mov": "video/quicktime"}
		mediaType := videoTypes[ext]
		if mediaType != "" && (declared == mediaType || declared == "application/octet-stream" || declared == "") {
			if !strings.HasPrefix(detected, "text/html") && detected != "image/svg+xml" {
				return mediaType, maxInstructionUpload, true
			}
		}
	case "file":
		return "application/octet-stream", 50 << 20, true
	}
	return "", 0, false
}

func (s *InstructionStore) upload(w http.ResponseWriter, r *http.Request, replaceID string) (InstructionBlock, error) {
	if replaceID != "" {
		s.mu.Lock()
		found := false
		for _, block := range s.blocks {
			if block.ID == replaceID && block.Kind != "text" {
				found = true
				break
			}
		}
		s.mu.Unlock()
		if !found {
			return InstructionBlock{}, os.ErrNotExist
		}
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxInstructionUpload+(1<<20))
	reader, err := r.MultipartReader()
	if err != nil {
		return InstructionBlock{}, fmt.Errorf("multipart upload required: %w", err)
	}
	var kind, title string
	var filePart io.Reader
	var fileName, declared string
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return InstructionBlock{}, err
		}
		switch part.FormName() {
		case "kind", "title":
			raw, err := io.ReadAll(io.LimitReader(part, 2048))
			if err != nil {
				return InstructionBlock{}, err
			}
			if part.FormName() == "kind" {
				kind = string(raw)
			} else {
				title = strings.TrimSpace(string(raw))
			}
		case "file":
			if filePart != nil {
				return InstructionBlock{}, fmt.Errorf("upload one file at a time")
			}
			filePart = part
			fileName = filepath.Base(strings.ReplaceAll(part.FileName(), "\\", "/"))
			fileName = strings.Map(func(r rune) rune {
				if r < 32 || r == 127 {
					return -1
				}
				return r
			}, fileName)
			declared, _, _ = mime.ParseMediaType(part.Header.Get("Content-Type"))
			// The file must be the final multipart part so it can be streamed.
			goto gotFile
		}
	}
gotFile:
	if filePart == nil {
		return InstructionBlock{}, fmt.Errorf("file field is missing from multipart upload")
	}
	if fileName == "" {
		return InstructionBlock{}, fmt.Errorf("uploaded file has no name")
	}
	if !validInstructionTitle(title) || len([]rune(fileName)) > 160 {
		return InstructionBlock{}, fmt.Errorf("title and file name may contain up to 160 characters")
	}
	var prefix [512]byte
	n, err := io.ReadFull(filePart, prefix[:])
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return InstructionBlock{}, err
	}
	if n == 0 {
		return InstructionBlock{}, fmt.Errorf("empty file")
	}
	mediaType, limit, ok := allowedInstructionMedia(kind, http.DetectContentType(prefix[:n]), declared, fileName)
	if !ok {
		return InstructionBlock{}, fmt.Errorf("unsupported file type for %s", kind)
	}
	id := replaceID
	if id == "" {
		id, err = newID()
		if err != nil {
			return InstructionBlock{}, err
		}
	}
	if err := os.MkdirAll(s.assetsDir, 0700); err != nil {
		return InstructionBlock{}, err
	}
	tmp, err := os.CreateTemp(s.assetsDir, ".upload-*")
	if err != nil {
		return InstructionBlock{}, err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if _, err := tmp.Write(prefix[:n]); err != nil {
		return InstructionBlock{}, err
	}
	copied, err := io.Copy(tmp, io.LimitReader(filePart, limit-int64(n)+1))
	if err != nil {
		return InstructionBlock{}, err
	}
	size := int64(n) + copied
	if size > limit {
		return InstructionBlock{}, fmt.Errorf("file exceeds the %d MB limit", limit>>20)
	}
	if err := tmp.Sync(); err != nil {
		return InstructionBlock{}, err
	}
	if err := tmp.Close(); err != nil {
		return InstructionBlock{}, err
	}
	block := InstructionBlock{ID: id, Kind: kind, Title: title, FileName: fileName, MediaType: mediaType, Size: size}
	s.mu.Lock()
	defer s.mu.Unlock()
	assetPath := filepath.Join(s.assetsDir, id)
	if replaceID != "" {
		next := append([]InstructionBlock{}, s.blocks...)
		index := -1
		for i := range next {
			if next[i].ID == id && next[i].Kind != "text" {
				index = i
				break
			}
		}
		if index < 0 {
			return InstructionBlock{}, os.ErrNotExist
		}
		backup, err := os.CreateTemp(s.assetsDir, ".previous-*")
		if err != nil {
			return InstructionBlock{}, err
		}
		backupPath := backup.Name()
		backup.Close()
		os.Remove(backupPath)
		if err := os.Rename(assetPath, backupPath); err != nil {
			return InstructionBlock{}, err
		}
		if err := os.Rename(tmp.Name(), assetPath); err != nil {
			_ = os.Rename(backupPath, assetPath)
			return InstructionBlock{}, err
		}
		next[index] = block
		if err := writeJSONFile(s.path, next); err != nil {
			_ = os.Remove(assetPath)
			_ = os.Rename(backupPath, assetPath)
			return InstructionBlock{}, err
		}
		s.blocks = next
		_ = os.Remove(backupPath)
		return block, nil
	}
	if err := os.Rename(tmp.Name(), assetPath); err != nil {
		return InstructionBlock{}, err
	}
	next := append(append([]InstructionBlock{}, s.blocks...), block)
	if err := writeJSONFile(s.path, next); err != nil {
		_ = os.Remove(assetPath)
		return InstructionBlock{}, err
	}
	s.blocks = next
	return block, nil
}

func (s *InstructionStore) serveAsset(w http.ResponseWriter, r *http.Request, id string) {
	s.mu.Lock()
	var block InstructionBlock
	for _, b := range s.blocks {
		if b.ID == id && b.Kind != "text" {
			block = b
			break
		}
	}
	s.mu.Unlock()
	if block.ID == "" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Type", block.MediaType)
	if block.Kind == "file" {
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": block.FileName}))
	}
	http.ServeFile(w, r, filepath.Join(s.assetsDir, id))
}

func (s *server) instructionAPI(w http.ResponseWriter, r *http.Request, user User) {
	if s.instructions == nil {
		apiError(w, http.StatusServiceUnavailable, "instructions unavailable")
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/instructions")
	if path == "" && r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, s.instructions.list())
		return
	}
	if strings.HasPrefix(path, "/assets/") && r.Method == http.MethodGet {
		s.instructions.serveAsset(w, r, strings.TrimPrefix(path, "/assets/"))
		return
	}
	if !requireAdmin(w, user) || !requireAction(w, r) {
		return
	}
	if path == "" && r.Method == http.MethodPost {
		var input struct {
			Title string `json:"title"`
			Body  string `json:"body"`
		}
		if err := decodeBody(w, r, &input); err != nil {
			apiError(w, http.StatusBadRequest, err.Error())
			return
		}
		block, err := s.instructions.addText(input.Title, input.Body)
		if err != nil {
			apiError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, block)
		return
	}
	if path == "/upload" && r.Method == http.MethodPost {
		block, err := s.instructions.upload(w, r, "")
		if err != nil {
			apiError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, block)
		return
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 2 && parts[1] == "asset" && r.Method == http.MethodPut {
		block, err := s.instructions.upload(w, r, parts[0])
		if err != nil {
			apiError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, block)
		return
	}
	if len(parts) == 1 && parts[0] != "" {
		if r.Method == http.MethodDelete {
			if err := s.instructions.delete(parts[0]); err != nil {
				apiError(w, http.StatusNotFound, "block not found")
				return
			}
			writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
			return
		}
		if r.Method == http.MethodPut {
			var input struct {
				Title string `json:"title"`
				Body  string `json:"body"`
			}
			if err := decodeBody(w, r, &input); err != nil {
				apiError(w, http.StatusBadRequest, err.Error())
				return
			}
			if err := s.instructions.update(parts[0], input.Title, input.Body); err != nil {
				apiError(w, http.StatusBadRequest, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
			return
		}
	}
	if len(parts) == 2 && parts[1] == "move" && r.Method == http.MethodPost {
		var input struct {
			Direction int `json:"direction"`
		}
		if err := decodeBody(w, r, &input); err != nil {
			apiError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := s.instructions.move(parts[0], input.Direction); err != nil {
			apiError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	http.NotFound(w, r)
}

