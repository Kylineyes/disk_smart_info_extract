package lint

import (
    "bufio"
    "bytes"
    "os"
    "os/exec"
    "path/filepath"
    "strings"
    "testing"
    "unicode/utf8"
)

const (
    maxSourceLineLength = 120
    goIndentWidth       = 4
)

func TestGoSourceUsesSpacesAndFitsLineLength(t *testing.T) {
    walkGoFiles(t, checkGoFileLines)
}

func TestGoSourceMatchesGofmtWithExpandedTabs(t *testing.T) {
    walkGoFiles(t, checkGoFileGofmtWithSpaces)
}

func walkGoFiles(t *testing.T, check func(*testing.T, string)) {
    t.Helper()
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
        check(t, path)
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
    data, err := os.ReadFile(path)
    if err != nil {
        t.Errorf("open %s: %v", path, err)
        return
    }
    relativePath := relativeGoPath(t, path)
    if bytes.Contains(data, []byte{'\t'}) {
        t.Errorf("%s contains tab characters; indent with %d spaces", relativePath, goIndentWidth)
    }

    scanner := bufio.NewScanner(bytes.NewReader(data))
    for lineNumber := 1; scanner.Scan(); lineNumber++ {
        line := strings.TrimSuffix(scanner.Text(), "\r")
        columns := visualColumns(line)
        if columns > maxSourceLineLength {
            t.Errorf("%s:%d has %d columns; maximum is %d", relativePath, lineNumber,
                columns, maxSourceLineLength)
        }
    }
    if err := scanner.Err(); err != nil {
        t.Errorf("read %s: %v", path, err)
    }
}

func checkGoFileGofmtWithSpaces(t *testing.T, path string) {
    t.Helper()
    data, err := os.ReadFile(path)
    if err != nil {
        t.Errorf("open %s: %v", path, err)
        return
    }
    formatted, err := exec.Command("gofmt", path).Output()
    if err != nil {
        t.Errorf("gofmt %s: %v", relativeGoPath(t, path), err)
        return
    }
    expanded := expandTabs(string(formatted), goIndentWidth)
    if expanded != string(data) {
        t.Errorf("%s is not gofmt output with tabs expanded to %d spaces",
            relativeGoPath(t, path), goIndentWidth)
    }
}

func relativeGoPath(t *testing.T, path string) string {
    t.Helper()
    relativePath, err := filepath.Rel(repositoryRoot(t), path)
    if err != nil {
        return path
    }
    return relativePath
}

func visualColumns(line string) int {
    return utf8.RuneCountInString(expandTabs(line, goIndentWidth))
}

func expandTabs(value string, tabWidth int) string {
    var builder strings.Builder
    column := 0
    for _, r := range value {
        if r == '\n' {
            builder.WriteRune(r)
            column = 0
            continue
        }
        if r == '\t' {
            spaces := tabWidth - (column % tabWidth)
            builder.WriteString(strings.Repeat(" ", spaces))
            column += spaces
            continue
        }
        builder.WriteRune(r)
        column++
    }
    return builder.String()
}
