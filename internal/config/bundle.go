package config

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ManifestFile holds captured bytes that are also used for parsing and hashing.
type ManifestFile struct {
	Name string
	Data []byte
}

// CapturedBundle identifies one captured manifest set, including an empty set.
type CapturedBundle struct {
	Revision string
	Files    []ManifestFile
	Digest   [sha256.Size]byte
}

// CaptureBundle pins a revision and reads bounded bytes exactly once.
func CaptureBundle(root string) (CapturedBundle, error) {
	return captureBundle(root, func(path string) ([]byte, error) {
		file, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		data, readErr := io.ReadAll(io.LimitReader(file, 1024*1024+1))
		closeErr := file.Close()
		if err := errors.Join(readErr, closeErr); err != nil {
			return nil, err
		}
		if len(data) > 1024*1024 {
			return nil, fmt.Errorf("file exceeds 1 MiB: %w", ErrInvalidConfig)
		}
		return data, nil
	})
}

func captureBundle(root string, read func(string) ([]byte, error)) (CapturedBundle, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return CapturedBundle{}, fmt.Errorf("resolve schedules path: %w", errors.Join(ErrInvalidConfig, err))
	}
	pinned, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return CapturedBundle{}, fmt.Errorf("resolve schedules directory: %w", errors.Join(ErrInvalidConfig, err))
	}
	dataLink := filepath.Join(pinned, "..data")
	if info, statErr := os.Lstat(dataLink); statErr == nil {
		if info.Mode()&os.ModeSymlink == 0 {
			return CapturedBundle{}, fmt.Errorf("ConfigMap ..data must be a symlink: %w", ErrInvalidConfig)
		}
		revision, resolveErr := filepath.EvalSymlinks(dataLink)
		if resolveErr != nil {
			return CapturedBundle{}, fmt.Errorf("resolve ConfigMap revision: %w", errors.Join(ErrInvalidConfig, resolveErr))
		}
		relative, relErr := filepath.Rel(pinned, revision)
		if relErr != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
			return CapturedBundle{}, fmt.Errorf("ConfigMap revision escapes schedules directory: %w", ErrInvalidConfig)
		}
		pinned = revision
	} else if !os.IsNotExist(statErr) {
		return CapturedBundle{}, fmt.Errorf("inspect ConfigMap ..data: %w", errors.Join(ErrInvalidConfig, statErr))
	}
	entries, err := os.ReadDir(pinned)
	if err != nil {
		return CapturedBundle{}, fmt.Errorf("read schedules directory: %w", errors.Join(ErrInvalidConfig, err))
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	bundle := CapturedBundle{Revision: pinned}
	var encoded []byte
	for _, entry := range entries {
		extension := strings.ToLower(filepath.Ext(entry.Name()))
		if extension != ".yaml" && extension != ".yml" {
			continue
		}
		if !entry.Type().IsRegular() {
			return CapturedBundle{}, fmt.Errorf("schedule file %q is not regular: %w", entry.Name(), ErrInvalidConfig)
		}
		data, err := read(filepath.Join(pinned, entry.Name()))
		if err != nil {
			return CapturedBundle{}, fmt.Errorf("read schedule file %q: %w", entry.Name(), errors.Join(ErrInvalidConfig, err))
		}
		if len(data) > 1024*1024 {
			return CapturedBundle{}, fmt.Errorf("schedule file %q exceeds 1 MiB: %w", entry.Name(), ErrInvalidConfig)
		}
		if len(bundle.Files) >= 4096 || len(encoded)+len(data)+len(entry.Name())+16 > 16*1024*1024 {
			return CapturedBundle{}, fmt.Errorf("schedules bundle exceeds 16 MiB or 4096 files: %w", ErrInvalidConfig)
		}
		bundle.Files = append(bundle.Files, ManifestFile{Name: entry.Name(), Data: data})
		encoded = binary.BigEndian.AppendUint64(encoded, uint64(len(entry.Name())))
		encoded = append(encoded, entry.Name()...)
		encoded = binary.BigEndian.AppendUint64(encoded, uint64(len(data)))
		encoded = append(encoded, data...)
	}
	bundle.Digest = sha256.Sum256(encoded)
	return bundle, nil
}
