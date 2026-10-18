package lint

import (
    "errors"
    "os"
    "os/exec"
    "path/filepath"
    "strings"
    "testing"
)

func TestFormatScriptChecksWithoutWritingAndFormatsIdempotently(t *testing.T) {
    source := "package main\nfunc main(){\n\tprintln(\"example\")\n}\n"
    script, path := formatScriptFixture(t, source)

    output := runFormatScript(t, script, 1, "--check")
    if !strings.Contains(output, "main.go") {
        t.Fatalf("check did not identify the unformatted file: %s", output)
    }
    assertFileContents(t, path, source)

    runFormatScript(t, script, 0)
    formatted := "package main\n\nfunc main() {\n    println(\"example\")\n}\n"
    assertFileContents(t, path, formatted)
    runFormatScript(t, script, 0, "--check")
    runFormatScript(t, script, 0)
    assertFileContents(t, path, formatted)
}

func TestFormatScriptChecksUnicodeLineWidth(t *testing.T) {
    for _, length := range []int{120, 121} {
        source := "package main\n\n// " + strings.Repeat("界", length-3) + "\n"
        script, path := formatScriptFixture(t, source)
        wantExit := 0
        if length > maxSourceLineLength {
            wantExit = 1
        }
        output := runFormatScript(t, script, wantExit, "--check")
        if wantExit != 0 && !strings.Contains(output, "maximum is 120") {
            t.Fatalf("missing line-width diagnostic: %s", output)
        }
        assertFileContents(t, path, source)
    }
}

func TestFormatScriptRejectsInvalidArguments(t *testing.T) {
    source := "package main\n"
    script, path := formatScriptFixture(t, source)
    output := runFormatScript(t, script, 2, "--unknown")
    if !strings.Contains(output, "usage:") {
        t.Fatalf("missing usage diagnostic: %s", output)
    }
    assertFileContents(t, path, source)
}

func TestFormatScriptRejectsInvalidGoSource(t *testing.T) {
    source := "not valid Go\n"
    script, path := formatScriptFixture(t, source)
    runFormatScript(t, script, 1, "--check")
    assertFileContents(t, path, source)
}

func TestFormatScriptSkipsGitAndVendorDirectories(t *testing.T) {
    script, path := formatScriptFixture(t, "package main\n")
    root := filepath.Dir(path)
    for _, directory := range []string{".git", "vendor"} {
        ignored := filepath.Join(root, directory, "invalid.go")
        if err := os.MkdirAll(filepath.Dir(ignored), 0o700); err != nil {
            t.Fatal(err)
        }
        if err := os.WriteFile(ignored, []byte("not valid Go\n"), 0o600); err != nil {
            t.Fatal(err)
        }
    }
    runFormatScript(t, script, 0, "--check")
}

func formatScriptFixture(t *testing.T, source string) (string, string) {
    t.Helper()
    contents, err := os.ReadFile(filepath.Join(repositoryRoot(t), "scripts", "format-go.sh"))
    if err != nil {
        t.Fatal(err)
    }
    root := filepath.Join(t.TempDir(), "project with spaces")
    script := filepath.Join(root, "scripts", "format-go.sh")
    if err := os.MkdirAll(filepath.Dir(script), 0o700); err != nil {
        t.Fatal(err)
    }
    if err := os.WriteFile(script, contents, 0o755); err != nil {
        t.Fatal(err)
    }
    path := filepath.Join(root, "main.go")
    if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
        t.Fatal(err)
    }
    return script, path
}

func runFormatScript(t *testing.T, script string, wantExit int, args ...string) string {
    t.Helper()
    command := exec.Command(script, args...)
    command.Dir = t.TempDir()
    output, err := command.CombinedOutput()
    gotExit := 0
    if err != nil {
        var exitError *exec.ExitError
        if !errors.As(err, &exitError) {
            t.Fatalf("run formatter: %v", err)
        }
        gotExit = exitError.ExitCode()
    }
    if gotExit != wantExit {
        t.Fatalf("formatter exit = %d, want %d; output: %s", gotExit, wantExit, output)
    }
    return string(output)
}

func assertFileContents(t *testing.T, path, want string) {
    t.Helper()
    contents, err := os.ReadFile(path)
    if err != nil {
        t.Fatal(err)
    }
    if string(contents) != want {
        t.Fatalf("%s contents = %q, want %q", path, contents, want)
    }
}

func TestExpandTabs(t *testing.T) {
    for _, test := range []struct {
        input string
        want  string
    }{
        {"", ""},
        {"\t", "    "},
        {"a\tb", "a   b"},
        {"abcd\t", "abcd    "},
        {"界\tb", "界   b"},
        {"a\tb\n\tc", "a   b\n    c"},
    } {
        if got := expandTabs(test.input, goIndentWidth); got != test.want {
            t.Errorf("expandTabs(%q) = %q, want %q", test.input, got, test.want)
        }
    }
}
