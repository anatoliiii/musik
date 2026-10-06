package library

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	MaxUploadMemory = 32 << 20 // 32 MiB in RAM, rest to temp files
	MaxUploadBytes  = 8 << 30  // 8 GiB per request
	MaxFileBytes    = 1 << 30  // 1 GiB per file
	MaxFilesPerReq  = 200
)

var audioExts = map[string]struct{}{
	".mp3": {}, ".flac": {}, ".m4a": {}, ".wav": {}, ".ogg": {}, ".opus": {},
}

type Saved struct {
	Path  string `json:"path"`
	Bytes int64  `json:"bytes"`
}

type Skipped struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

func SanitizeRelPath(raw string) (string, error) {
	raw = strings.TrimSpace(strings.ReplaceAll(raw, "\\", "/"))
	raw = strings.TrimPrefix(raw, "/")
	if raw == "" {
		return "", errors.New("пустое имя")
	}
	parts := strings.Split(raw, "/")
	clean := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" || p == "." {
			continue
		}
		if p == ".." {
			return "", errors.New("недопустимый путь")
		}
		if strings.HasPrefix(p, ".") {
			return "", errors.New("скрытый путь")
		}
		if strings.ContainsRune(p, 0) {
			return "", errors.New("недопустимое имя")
		}
		clean = append(clean, p)
	}
	if len(clean) == 0 {
		return "", errors.New("пустое имя")
	}
	rel := filepath.Join(clean...)
	ext := strings.ToLower(filepath.Ext(rel))
	if _, ok := audioExts[ext]; !ok {
		return "", errors.New("неподдерживаемый формат")
	}
	return rel, nil
}

func ResolveDest(root, rel string) (string, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", errors.New("библиотека недоступна")
	}
	dest := filepath.Join(absRoot, rel)
	absDest, err := filepath.Abs(dest)
	if err != nil {
		return "", errors.New("недопустимый путь")
	}
	relToRoot, err := filepath.Rel(absRoot, absDest)
	if err != nil || strings.HasPrefix(relToRoot, "..") || filepath.IsAbs(relToRoot) {
		return "", errors.New("недопустимый путь")
	}
	return absDest, nil
}

func SaveFile(root, rel string, src io.Reader) (int64, error) {
	dest, err := ResolveDest(root, rel)
	if err != nil {
		return 0, err
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return 0, err
	}
	relative, err := filepath.Rel(absoluteRoot, dest)
	if err != nil {
		return 0, err
	}
	contained, err := os.OpenRoot(absoluteRoot)
	if err != nil {
		return 0, errors.New("библиотека недоступна")
	}
	defer contained.Close()
	if err := contained.MkdirAll(filepath.Dir(relative), 0o755); err != nil {
		return 0, errors.New("не удалось создать папку")
	}

	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return 0, err
	}
	tmpName := filepath.Join(filepath.Dir(relative), ".upload-"+hex.EncodeToString(nonce[:])+".part")
	tmp, err := contained.OpenFile(tmpName, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return 0, errors.New("не удалось создать временный файл")
	}
	cleanup := true
	defer func() {
		_ = tmp.Close()
		if cleanup {
			_ = contained.Remove(tmpName)
		}
	}()

	n, err := io.Copy(tmp, io.LimitReader(src, MaxFileBytes+1))
	if err != nil {
		return 0, errors.New("ошибка записи")
	}
	if n > MaxFileBytes {
		return 0, errors.New("файл слишком большой")
	}
	if err := tmp.Chmod(0644); err != nil {
		return 0, errors.New("ошибка прав файла")
	}
	if err := tmp.Close(); err != nil {
		return 0, errors.New("ошибка записи")
	}
	if err := contained.Rename(tmpName, relative); err != nil {
		return 0, errors.New("не удалось сохранить файл")
	}
	cleanup = false
	return n, nil
}
