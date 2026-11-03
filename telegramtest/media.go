package telegramtest

import (
	"bytes"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"time"
)

type platformFile struct {
	path    string
	content []byte
}

// AddFile 复制平台文件，供 getFile 与文件下载使用。
func (s *Server) AddFile(fileID, filePath string, content []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.files == nil {
		s.files = make(map[string]platformFile)
	}
	s.files[fileID] = platformFile{path: filePath, content: slices.Clone(content)}
}

func (s *Server) getFile(w http.ResponseWriter, r *http.Request) {
	var request struct {
		ID string `json:"file_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		s.respond(w, http.StatusBadRequest, nil)
		return
	}
	s.mu.Lock()
	file, ok := s.files[request.ID]
	s.mu.Unlock()
	if !ok {
		s.respond(w, http.StatusBadRequest, nil)
		return
	}
	s.respond(w, http.StatusOK, map[string]any{"file_id": request.ID, "file_path": file.path, "file_size": len(file.content)})
}

func (s *Server) download(w http.ResponseWriter, r *http.Request) {
	filePath := strings.TrimPrefix(r.URL.Path, "/file/bot"+s.token+"/")
	s.mu.Lock()
	var file platformFile
	found := false
	for _, candidate := range s.files {
		if candidate.path == filePath {
			file, found = candidate, true
			break
		}
	}
	s.mu.Unlock()
	if !found {
		s.respond(w, http.StatusNotFound, nil)
		return
	}
	http.ServeContent(w, r, filePath, time.Time{}, bytes.NewReader(file.content))
}
