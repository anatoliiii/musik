package library

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const MaxArchiveEntries = 2000
const MaxArchiveBytes int64 = 8 << 30

func IsArchive(name string) bool {
	name = strings.ToLower(name)
	return strings.HasSuffix(name, ".zip") || strings.HasSuffix(name, ".tar") ||
		strings.HasSuffix(name, ".tar.gz") || strings.HasSuffix(name, ".tgz")
}

// SaveArchive accepts regular audio files only. Extraction is staged before
// publishing; links, absolute paths, traversal and excessive expansion fail.
func SaveArchive(root, name string, src io.Reader) ([]Saved, []Skipped, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, nil, err
	}
	stage, err := os.MkdirTemp(root, ".archive-*")
	if err != nil {
		return nil, nil, err
	}
	defer os.RemoveAll(stage)
	compressed, err := os.CreateTemp("", "musik-archive-*")
	if err != nil {
		return nil, nil, err
	}
	defer os.Remove(compressed.Name())
	defer compressed.Close()
	n, err := io.Copy(compressed, io.LimitReader(src, MaxFileBytes+1))
	if err != nil || n > MaxFileBytes {
		return nil, nil, errors.New("архив повреждён или превышает 1 ГиБ")
	}
	if _, err := compressed.Seek(0, io.SeekStart); err != nil {
		return nil, nil, err
	}
	var extracted []Saved
	var skipped []Skipped
	var expanded int64
	seen := make(map[string]bool)
	save := func(raw string, size int64, reader io.Reader) error {
		raw = strings.ReplaceAll(raw, "\\", "/")
		if strings.HasPrefix(raw, "/") || strings.Contains(raw, ":") {
			return errors.New("недопустимый путь в архиве")
		}
		for _, part := range strings.Split(raw, "/") {
			if part == ".." {
				return errors.New("недопустимый путь в архиве")
			}
		}
		if size < 0 || size > MaxArchiveBytes-expanded {
			return errors.New("распакованный архив превышает 8 ГиБ")
		}
		expanded += size
		rel, err := SanitizeRelPath(raw)
		if err != nil {
			skipped = append(skipped, Skipped{Path: raw, Reason: err.Error()})
			return nil
		}
		if seen[rel] {
			return errors.New("повторяющийся путь в архиве")
		}
		seen[rel] = true
		if size > MaxFileBytes {
			return errors.New("аудиофайл в архиве превышает 1 ГиБ")
		}
		written, err := SaveFile(stage, rel, reader)
		if err != nil {
			return err
		}
		if written != size {
			return errors.New("повреждённый файл в архиве")
		}
		extracted = append(extracted, Saved{Path: rel, Bytes: written})
		return nil
	}
	if strings.HasSuffix(strings.ToLower(name), ".zip") {
		archive, err := zip.NewReader(compressed, n)
		if err != nil {
			return nil, nil, errors.New("не удалось прочитать ZIP")
		}
		if len(archive.File) > MaxArchiveEntries {
			return nil, nil, errors.New("в архиве больше 2000 записей")
		}
		for _, entry := range archive.File {
			if entry.FileInfo().IsDir() {
				continue
			}
			if !entry.Mode().IsRegular() {
				return nil, nil, errors.New("ссылки и специальные файлы в архиве запрещены")
			}
			if entry.UncompressedSize64 > uint64(MaxArchiveBytes) {
				return nil, nil, errors.New("слишком большой файл в архиве")
			}
			r, err := entry.Open()
			if err != nil {
				return nil, nil, err
			}
			err = save(entry.Name, int64(entry.UncompressedSize64), r)
			_ = r.Close()
			if err != nil {
				return nil, nil, err
			}
		}
	} else {
		var reader io.Reader = compressed
		lower := strings.ToLower(name)
		if strings.HasSuffix(lower, ".gz") || strings.HasSuffix(lower, ".tgz") {
			gz, err := gzip.NewReader(compressed)
			if err != nil {
				return nil, nil, errors.New("не удалось прочитать GZIP")
			}
			defer gz.Close()
			reader = gz
		}
		// Includes headers and skipped payloads in the decompression limit.
		limited := &io.LimitedReader{R: reader, N: MaxArchiveBytes + 1}
		archive := tar.NewReader(limited)
		for count := 0; ; count++ {
			entry, err := archive.Next()
			if limited.N <= 0 {
				return nil, nil, errors.New("распакованный архив превышает 8 ГиБ")
			}
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, nil, errors.New("повреждённый TAR")
			}
			if count >= MaxArchiveEntries {
				return nil, nil, errors.New("в архиве больше 2000 записей")
			}
			if entry.Typeflag == tar.TypeDir {
				continue
			}
			if entry.Typeflag != tar.TypeReg && entry.Typeflag != tar.TypeRegA {
				return nil, nil, errors.New("ссылки и специальные файлы в архиве запрещены")
			}
			if err := save(entry.Name, entry.Size, archive); err != nil {
				return nil, nil, err
			}
		}
		// Consume the gzip trailer too, so a bad checksum cannot publish files.
		if _, err := io.Copy(io.Discard, limited); err != nil {
			return nil, nil, errors.New("повреждённый архив")
		}
		if limited.N <= 0 {
			return nil, nil, errors.New("распакованный архив превышает 8 ГиБ")
		}
	}
	var saved []Saved
	for _, file := range extracted {
		r, err := os.Open(filepath.Join(stage, file.Path))
		if err != nil {
			return saved, skipped, err
		}
		n, err := SaveFile(root, file.Path, r)
		_ = r.Close()
		if err != nil {
			return saved, skipped, err
		}
		saved = append(saved, Saved{Path: file.Path, Bytes: n})
	}
	return saved, skipped, nil
}
