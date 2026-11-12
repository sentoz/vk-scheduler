package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBundlePinsRevisionAcrossPointerSwitch(t *testing.T) {
	root := t.TempDir()
	old, fresh := filepath.Join(root, "old"), filepath.Join(root, "fresh")
	for _, directory := range []string{old, fresh} {
		if err := os.Mkdir(directory, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"a.yaml", "b.yaml"} {
		if err := os.WriteFile(filepath.Join(old, name), []byte("old"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(fresh, name), []byte("fresh"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(root, "current")
	if err := os.Symlink(old, link); err != nil {
		t.Fatal(err)
	}
	reads := 0
	bundle, err := captureBundle(link, func(path string) ([]byte, error) {
		reads++
		data, err := os.ReadFile(path)
		if reads == 1 {
			replacement := filepath.Join(root, "replacement")
			if err := os.Symlink(fresh, replacement); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(replacement, link); err != nil {
				t.Fatal(err)
			}
		}
		return data, err
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Files) != 2 {
		t.Fatal("missing captured files")
	}
	for _, file := range bundle.Files {
		if string(file.Data) != "old" {
			t.Fatal("mixed revision bytes")
		}
	}
	current, err := CaptureBundle(link)
	if err != nil {
		t.Fatal(err)
	}
	if current.Digest == bundle.Digest {
		t.Fatal("different bytes had same applied digest")
	}
}

func TestCaptureFailuresKeepContextAndCauses(t *testing.T) {
	directory := t.TempDir()
	if _, err := CaptureBundle(filepath.Join(directory, "missing")); !errors.Is(err, ErrInvalidConfig) || !errors.Is(err, os.ErrNotExist) {
		t.Fatal("missing directory error lost context", err)
	}
	path := filepath.Join(directory, "large.yaml")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", (1<<20)+1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := CaptureBundle(directory); !errors.Is(err, ErrInvalidConfig) || !strings.Contains(err.Error(), "large.yaml") || !strings.Contains(err.Error(), "1 MiB") {
		t.Fatal("file size error is not actionable", err)
	}
	cause := errors.New("synthetic read failure")
	if _, err := captureBundle(directory, func(string) ([]byte, error) { return nil, cause }); !errors.Is(err, cause) || !errors.Is(err, ErrInvalidConfig) {
		t.Fatal("read cause was discarded", err)
	}
}

func TestEmptyBundleIsValid(t *testing.T) {
	bundle, err := CaptureBundle(t.TempDir())
	if err != nil || len(bundle.Files) != 0 {
		t.Fatalf("bundle=%+v err=%v", bundle, err)
	}
}
