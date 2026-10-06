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

// archiveSource is satisfied by multipart.File, which is already spooled to
// memory or a temporary file; it is read in place instead of copied again.
type archiveSource interface {
	io.Reader
	io.ReaderAt
	io.Seeker
}

// SaveArchive accepts regular audio files only. Extraction is staged inside
// the library (a hidden directory the scanner ignores) and published by rename
// once the whole archive is valid; links, absolute paths, traversal and
// excessive expansion fail before anything becomes visible.
func SaveArchive(root, name string, src io.Reader) ([]Saved, []Skipped, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, nil, err
	}
	input, size, cleanup, err := openArchiveSource(src)
	if err != nil {
		return nil, nil, err
	}
	defer cleanup()
	stage, err := os.MkdirTemp(root, ".archive-*")
	if err != nil {
		return nil, nil, err
	}
	defer os.RemoveAll(stage)

	var extracted []Saved
	var skipped []Skipped
	var expanded int64
	seen := make(map[string]bool)
	save := func(raw string, size int64, reader io.Reader) error {
		raw = strings.ReplaceAll(raw, "\\", "/")
		if strings.HasPrefix(raw, "/") || hasDriveLetter(raw) {
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
		archive, err := zip.NewReader(input, size)
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
		var reader io.Reader = input
		lower := strings.ToLower(name)
		if strings.HasSuffix(lower, ".gz") || strings.HasSuffix(lower, ".tgz") {
			gz, err := gzip.NewReader(input)
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
	if err := publishStaged(root, stage, extracted); err != nil {
		return nil, skipped, err
	}
	return extracted, skipped, nil
}

// hasDriveLetter rejects Windows absolute paths ("C:/...") while allowing
// colons inside ordinary names such as "Artist: Title.flac".
func hasDriveLetter(path string) bool {
	if len(path) < 2 || path[1] != ':' {
		return false
	}
	c := path[0]
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func openArchiveSource(src io.Reader) (archiveSource, int64, func(), error) {
	if file, ok := src.(archiveSource); ok {
		size, err := file.Seek(0, io.SeekEnd)
		if err == nil {
			_, err = file.Seek(0, io.SeekStart)
		}
		if err != nil {
			return nil, 0, nil, err
		}
		if size > MaxFileBytes {
			return nil, 0, nil, errors.New("архив превышает 1 ГиБ")
		}
		return file, size, func() {}, nil
	}
	spool, err := os.CreateTemp("", "musik-archive-*")
	if err != nil {
		return nil, 0, nil, err
	}
	cleanup := func() {
		_ = spool.Close()
		_ = os.Remove(spool.Name())
	}
	n, err := io.Copy(spool, io.LimitReader(src, MaxFileBytes+1))
	if err != nil || n > MaxFileBytes {
		cleanup()
		return nil, 0, nil, errors.New("архив повреждён или превышает 1 ГиБ")
	}
	if _, err := spool.Seek(0, io.SeekStart); err != nil {
		cleanup()
		return nil, 0, nil, err
	}
	return spool, n, cleanup, nil
}

// publishStaged moves staged files into the library. The stage lives on the
// same filesystem, so each move is a rename; a failure removes the files this
// archive created, leaving previously existing library files in place.
func publishStaged(root, stage string, files []Saved) error {
	library, err := os.OpenRoot(root)
	if err != nil {
		return errors.New("библиотека недоступна")
	}
	defer library.Close()
	stageRel, err := filepath.Rel(root, stage)
	if err != nil {
		return err
	}
	var created []string
	rollback := func() {
		for _, rel := range created {
			_ = library.Remove(rel)
		}
	}
	for _, file := range files {
		if err := library.MkdirAll(filepath.Dir(file.Path), 0o755); err != nil {
			rollback()
			return errors.New("не удалось создать папку")
		}
		_, statErr := library.Lstat(file.Path)
		if err := library.Rename(filepath.Join(stageRel, file.Path), file.Path); err != nil {
			rollback()
			return errors.New("не удалось сохранить файл")
		}
		if errors.Is(statErr, os.ErrNotExist) {
			created = append(created, file.Path)
		}
	}
	return nil
}
