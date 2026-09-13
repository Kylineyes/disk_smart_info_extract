package smartctl

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

const helperProcessEnvironment = "SMARTCTL_TEST_HELPER_PROCESS"

// The copied test binary is used as a temporary smartctl executable. Running
// this before the testing package parses flags lets the helper receive the
// exact arguments that Run passes to the executable.
func init() {
	if os.Getenv(helperProcessEnvironment) != "1" ||
		smartctlExecutableName(os.Args[0]) != "smartctl" {
		return
	}
	if len(os.Args) != 3 || os.Args[1] != "--all" || os.Args[2] != os.Getenv("SMARTCTL_TEST_DEVICE") {
		fmt.Fprint(os.Stderr, "unexpected smartctl arguments")
		os.Exit(64)
	}
	fmt.Fprint(os.Stdout, os.Getenv("SMARTCTL_TEST_STDOUT"))
	fmt.Fprint(os.Stderr, os.Getenv("SMARTCTL_TEST_STDERR"))

	exitCode := 0
	if value := os.Getenv("SMARTCTL_TEST_EXIT"); value != "" {
		var err error
		exitCode, err = strconv.Atoi(value)
		if err != nil {
			fmt.Fprint(os.Stderr, "invalid helper exit code")
			os.Exit(64)
		}
	}
	os.Exit(exitCode)
}

func TestRunReturnsStdoutAndPassesArguments(t *testing.T) {
	installFakeSmartctl(t)
	const (
		device = "/dev/nvme0"
		stdout = "SMART/Health Information\nmodel: test-drive\n"
		stderr = "diagnostic output must stay out of the report"
	)
	t.Setenv("SMARTCTL_TEST_DEVICE", device)
	t.Setenv("SMARTCTL_TEST_STDOUT", stdout)
	t.Setenv("SMARTCTL_TEST_STDERR", stderr)
	t.Setenv("SMARTCTL_TEST_EXIT", "0")

	got, err := Run(context.Background(), device)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got != stdout {
		t.Fatalf("Run() stdout = %q, want %q", got, stdout)
	}
	if strings.Contains(got, stderr) {
		t.Fatalf("Run() included stderr in stdout: %q", got)
	}
}

func TestRunExitFailureDoesNotLeakOutput(t *testing.T) {
	installFakeSmartctl(t)
	const (
		device = "/dev/nvme0"
		stdout = "private stdout from failed command"
		stderr = "private stderr from failed command"
	)
	t.Setenv("SMARTCTL_TEST_DEVICE", device)
	t.Setenv("SMARTCTL_TEST_STDOUT", stdout)
	t.Setenv("SMARTCTL_TEST_STDERR", stderr)
	t.Setenv("SMARTCTL_TEST_EXIT", "42")

	got, err := Run(context.Background(), device)
	if err == nil {
		t.Fatal("Run() unexpectedly succeeded")
	}
	if got != "" {
		t.Fatalf("Run() returned failed command stdout: %q", got)
	}
	if !strings.Contains(err.Error(), "smartctl --all exited unsuccessfully") {
		t.Fatalf("Run() error lacks command context: %v", err)
	}
	if strings.Contains(err.Error(), stdout) || strings.Contains(err.Error(), stderr) {
		t.Fatalf("Run() error leaked command output: %v", err)
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("Run() error does not wrap exec.ExitError: %v", err)
	}
	if len(exitErr.Stderr) != 0 {
		t.Fatalf("Run() retained stderr in exec.ExitError: %q", exitErr.Stderr)
	}
}

func TestRunRejectsEmptyOrWhitespaceDevice(t *testing.T) {
	for _, device := range []string{"", " ", "\t\n"} {
		t.Run("device_"+strings.ReplaceAll(device, "\t", "tab"), func(t *testing.T) {
			got, err := Run(context.Background(), device)
			if err == nil {
				t.Fatal("Run() unexpectedly succeeded")
			}
			if got != "" {
				t.Fatalf("Run() stdout = %q for invalid device", got)
			}
			if !strings.Contains(err.Error(), "device must not be empty") {
				t.Fatalf("Run() error = %v", err)
			}
		})
	}
}

func TestRunPreservesCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	got, err := Run(ctx, "/dev/nvme0")
	if got != "" {
		t.Fatalf("Run() stdout = %q for canceled context", got)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
}

func TestRunPreservesDeadlineExceeded(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-ctx.Done()

	got, err := Run(ctx, "/dev/nvme0")
	if got != "" {
		t.Fatalf("Run() stdout = %q for expired context", got)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run() error = %v, want context.DeadlineExceeded", err)
	}
}

func TestRunLookupFailureIsContextual(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	got, err := Run(context.Background(), "/dev/nvme0")
	if err == nil {
		t.Fatal("Run() unexpectedly succeeded")
	}
	if got != "" {
		t.Fatalf("Run() stdout = %q after lookup failure", got)
	}
	if !strings.Contains(err.Error(), "look up smartctl") {
		t.Fatalf("Run() error = %v, want lookup context", err)
	}
}

func TestRunStartFailureIsContextual(t *testing.T) {
	fakePath := installFakeSmartctl(t)
	if err := os.WriteFile(fakePath, []byte("not an executable format"), 0o700); err != nil {
		t.Fatalf("replace fake smartctl: %v", err)
	}

	got, err := Run(context.Background(), "/dev/nvme0")
	if err == nil {
		t.Fatal("Run() unexpectedly succeeded")
	}
	if got != "" {
		t.Fatalf("Run() stdout = %q after start failure", got)
	}
	if !strings.Contains(err.Error(), "start smartctl --all") {
		t.Fatalf("Run() error = %v, want start context", err)
	}
}

func installFakeSmartctl(t *testing.T) string {
	t.Helper()

	directory := t.TempDir()
	name := "smartctl"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(directory, name)
	testBinary, err := os.Executable()
	if err != nil {
		t.Fatalf("find test binary: %v", err)
	}
	contents, err := os.ReadFile(testBinary)
	if err != nil {
		t.Fatalf("read test binary: %v", err)
	}
	if err := os.WriteFile(path, contents, 0o700); err != nil {
		t.Fatalf("write fake smartctl: %v", err)
	}
	t.Setenv("PATH", directory)
	t.Setenv(helperProcessEnvironment, "1")
	return path
}

func smartctlExecutableName(path string) string {
	name := filepath.Base(path)
	return strings.TrimSuffix(name, filepath.Ext(name))
}
