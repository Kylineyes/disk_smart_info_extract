package lint

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

const maxSourceLineLength = 120

func TestGoSourceLineLength(t *testing.T) {
	root := repositoryRoot(t)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == ".git" || entry.Name() == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" {
			return nil
		}
		checkGoFileLines(t, path)
		return nil
	})
	if err != nil {
		t.Fatalf("walk repository: %v", err)
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	for directory := workingDirectory; ; directory = filepath.Dir(directory) {
		if _, err := os.Stat(filepath.Join(directory, "go.mod")); err == nil {
			return directory
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			t.Fatal("could not locate repository root")
		}
	}
}

func checkGoFileLines(t *testing.T, path string) {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Errorf("open %s: %v", path, err)
		return
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for lineNumber := 1; scanner.Scan(); lineNumber++ {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if utf8.RuneCountInString(line) > maxSourceLineLength {
			relativePath, err := filepath.Rel(repositoryRoot(t), path)
			if err != nil {
				relativePath = path
			}
			t.Errorf("%s:%d has %d characters; maximum is %d", relativePath, lineNumber,
				utf8.RuneCountInString(line), maxSourceLineLength)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Errorf("read %s: %v", path, err)
	}
}
