package api

import (
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/torwin-job/musik/player/internal/library"
)

func (s *Server) handleLibraryUpload(w http.ResponseWriter, r *http.Request) {
	root := strings.TrimSpace(s.Cfg.Library)
	if root == "" {
		writeErr(w, 500, "no_library", "MUSIK_LIBRARY не задан")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, library.MaxUploadBytes)
	if err := r.ParseMultipartForm(library.MaxUploadMemory); err != nil {
		writeErr(w, 400, "bad_multipart", "не удалось прочитать файлы")
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}

	headers := r.MultipartForm.File["file"]
	if len(headers) == 0 {
		headers = r.MultipartForm.File["files"]
	}
	if len(headers) == 0 {
		writeErr(w, 400, "no_files", "нет файлов")
		return
	}
	if len(headers) > library.MaxFilesPerReq {
		writeErr(w, 400, "too_many", fmt.Sprintf("не больше %d файлов за раз", library.MaxFilesPerReq))
		return
	}
	paths := r.MultipartForm.Value["path"]

	if err := os.MkdirAll(root, 0o755); err != nil {
		writeErr(w, 500, "mkdir", "не удалось создать библиотеку")
		return
	}

	saved := make([]library.Saved, 0, len(headers))
	skipped := make([]library.Skipped, 0)
	for i, hdr := range headers {
		raw := hdr.Filename
		if i < len(paths) && strings.TrimSpace(paths[i]) != "" {
			raw = paths[i]
		}
		if library.IsArchive(raw) {
			src, err := hdr.Open()
			if err != nil {
				skipped = append(skipped, library.Skipped{Path: raw, Reason: "не удалось открыть архив"})
				continue
			}
			files, ignored, err := library.SaveArchive(root, raw, src)
			_ = src.Close()
			saved = append(saved, files...)
			skipped = append(skipped, ignored...)
			if err != nil {
				skipped = append(skipped, library.Skipped{Path: raw, Reason: err.Error()})
			}
			continue
		}
		rel, err := library.SanitizeRelPath(raw)
		if err != nil {
			skipped = append(skipped, library.Skipped{Path: raw, Reason: err.Error()})
			continue
		}
		if hdr.Size > 0 && hdr.Size > library.MaxFileBytes {
			skipped = append(skipped, library.Skipped{Path: rel, Reason: "файл слишком большой"})
			continue
		}
		src, err := hdr.Open()
		if err != nil {
			skipped = append(skipped, library.Skipped{Path: rel, Reason: "не удалось открыть файл"})
			continue
		}
		n, err := library.SaveFile(root, rel, src)
		_ = src.Close()
		if err != nil {
			skipped = append(skipped, library.Skipped{Path: rel, Reason: err.Error()})
			continue
		}
		saved = append(saved, library.Saved{Path: rel, Bytes: n})
	}

	writeJSON(w, map[string]any{
		"ok":      true,
		"saved":   saved,
		"skipped": skipped,
		"count":   len(saved),
	})
}
