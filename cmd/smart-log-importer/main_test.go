package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"smart-log-importer/internal/config"
	"smart-log-importer/internal/model"
	"smart-log-importer/internal/parser"
	"smart-log-importer/internal/store"
)

type fakeStore struct {
	initialized bool
	upserted    bool
	closed      bool
	log         model.SmartLog
}

func (s *fakeStore) Initialize(ctx context.Context) error {
	if _, ok := ctx.Deadline(); !ok {
		return errors.New("initialize context has no deadline")
	}
	s.initialized = true
	return nil
}

func (s *fakeStore) Upsert(ctx context.Context, log model.SmartLog) error {
	if _, ok := ctx.Deadline(); !ok {
		return errors.New("upsert context has no deadline")
	}
	s.upserted = true
	s.log = log
	return nil
}

func (s *fakeStore) Close() {
	s.closed = true
}

func TestRunSuccessSeparatesOutputAndRedactsSensitiveValues(t *testing.T) {
	const password = "super-secret-password"
	const serial = "NVME-SERIAL-123456"
	const rawLog = "private SMART report for NVME-SERIAL-123456"
	fake := new(fakeStore)
	deps := importerDeps{
		loadDBConfig: func(string) (config.DBConfig, error) {
			return config.DBConfig{
				Type:     "postgres",
				Host:     "db",
				Port:     5432,
				User:     "importer",
				Password: password,
				Database: "smart",
				Table:    "smart_log",
			}, nil
		},
		parseNVMeFile: func(path string) (model.SmartLog, error) {
			return model.SmartLog{
				SnapshotDate: time.Date(2026, time.August, 31, 0, 0, 0, 0, time.UTC),
				Device:       model.DeviceInfo{Model: "Example NVMe 4TB", Serial: serial},
				Health:       model.HealthInfo{OverallHealth: "PASSED"},
				RawLog:       rawLog,
			}, nil
		},
		openStore: func(ctx context.Context, cfg config.DBConfig, _ *slog.Logger) (store.Store, error) {
			if _, ok := ctx.Deadline(); !ok {
				return nil, errors.New("open context has no deadline")
			}
			return fake, nil
		},
	}

	var stdout, stderr strings.Builder
	args := []string{"-c", "db.yaml", "-f", "report.log", "-log-format", "json"}
	if err := runWithDeps(args, &stdout, &stderr, deps); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if !fake.initialized || !fake.upserted || !fake.closed {
		t.Fatalf("store lifecycle = initialized:%v upserted:%v closed:%v", fake.initialized, fake.upserted, fake.closed)
	}
	if got := stdout.String(); !strings.Contains(got, `imported smart log: date=2026-08-31`) ||
		!strings.Contains(got, `serial="...3456"`) {
		t.Fatalf("unexpected stdout: %q", got)
	}
	if strings.Contains(stdout.String(), serial) || strings.Contains(stdout.String(), password) ||
		strings.Contains(stdout.String(), rawLog) {
		t.Fatalf("sensitive value in stdout: %q", stdout.String())
	}
	if strings.Contains(stderr.String(), serial) || strings.Contains(stderr.String(), password) ||
		strings.Contains(stderr.String(), rawLog) {
		t.Fatalf("sensitive value in stderr: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), `"file":"report.log"`) || strings.Contains(stderr.String(), `"device":`) {
		t.Fatalf("file mode did not retain only file input metadata: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), `"msg":"import started"`) ||
		!strings.Contains(stderr.String(), `"msg":"smart log upserted"`) {
		t.Fatalf("expected structured events in stderr: %q", stderr.String())
	}
}

func TestRunMissingRequiredFlagDoesNotStartImport(t *testing.T) {
	called := false
	deps := importerDeps{
		loadDBConfig: func(string) (config.DBConfig, error) {
			called = true
			return config.DBConfig{}, nil
		},
		parseNVMeFile: func(string) (model.SmartLog, error) { return model.SmartLog{}, nil },
		openStore: func(context.Context, config.DBConfig, *slog.Logger) (store.Store, error) {
			return nil, nil
		},
	}
	var stdout, stderr strings.Builder
	if err := runWithDeps([]string{"-f", "report.log"}, &stdout, &stderr, deps); err == nil {
		t.Fatal("run() succeeded without -c")
	}
	if called {
		t.Fatal("configuration loader called for invalid arguments")
	}
	if stdout.Len() != 0 || !strings.Contains(stderr.String(), "usage:") || !strings.Contains(stderr.String(), "-c") {
		t.Fatalf("unexpected argument output: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestRunFailureRedactsSerialFromStoreError(t *testing.T) {
	const password = "super-secret-password"
	const serial = "NVME-SERIAL-123456"
	deps := importerDeps{
		loadDBConfig: func(string) (config.DBConfig, error) {
			return config.DBConfig{
				Type:     "postgres",
				Host:     "db",
				Port:     5432,
				User:     "importer",
				Password: password,
				Database: "smart",
				Table:    "smart_log",
			}, nil
		},
		parseNVMeFile: func(path string) (model.SmartLog, error) {
			return model.SmartLog{
				SnapshotDate: time.Date(2026, time.August, 31, 0, 0, 0, 0, time.UTC),
				Device:       model.DeviceInfo{Model: "Example NVMe 4TB", Serial: serial},
				Health:       model.HealthInfo{OverallHealth: "PASSED"},
			}, nil
		},
		openStore: func(context.Context, config.DBConfig, *slog.Logger) (store.Store, error) {
			return nil, fmt.Errorf("database rejected device serial %s", serial)
		},
	}

	var stdout, stderr strings.Builder
	if err := runWithDeps([]string{"-c", "db.yaml", "-f", "report.log"}, &stdout, &stderr, deps); err == nil {
		t.Fatal("run() unexpectedly succeeded")
	}
	if stdout.Len() != 0 {
		t.Fatalf("failure wrote success output: %q", stdout.String())
	}
	if strings.Contains(stderr.String(), serial) || strings.Contains(stderr.String(), password) {
		t.Fatalf("failure log leaked a sensitive value: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "[redacted]") {
		t.Fatalf("failure log did not redact sensitive data: %q", stderr.String())
	}
}

func TestRunFailureDoesNotWriteSuccessSummary(t *testing.T) {
	deps := importerDeps{
		loadDBConfig: func(string) (config.DBConfig, error) {
			return config.DBConfig{}, errors.New("configuration unavailable")
		},
		parseNVMeFile: func(string) (model.SmartLog, error) { return model.SmartLog{}, nil },
		openStore: func(context.Context, config.DBConfig, *slog.Logger) (store.Store, error) {
			return nil, nil
		},
	}
	var stdout, stderr strings.Builder
	if err := runWithDeps([]string{"-c", "db.yaml", "-f", "report.log"}, &stdout, &stderr, deps); err == nil {
		t.Fatal("run() unexpectedly succeeded")
	}
	if stdout.Len() != 0 {
		t.Fatalf("failure wrote success output: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "import failed") ||
		!strings.Contains(stderr.String(), "configuration unavailable") {
		t.Fatalf("missing failure diagnostic: %q", stderr.String())
	}
}

func TestRunDeviceModeUsesBoundedCollectionAndParser(t *testing.T) {
	const (
		device = "/dev/nvme0"
		serial = "LIVE-SERIAL-9876"
		rawLog = "smartctl 7.5\n" +
			"Model Number: Live NVMe\n" +
			"Serial Number: LIVE-SERIAL-9876\n" +
			"Local Time is: Mon Aug 31 08:00:01 2026 CST\n" +
			"SMART overall-health self-assessment test result: PASSED\n" +
			"SMART/Health Information (NVMe Log 0x02)\n"
	)
	fake := new(fakeStore)
	calledSmartctl := false
	calledParser := false
	deps := importerDeps{
		loadDBConfig: func(string) (config.DBConfig, error) {
			return config.DBConfig{
				Type: "postgres", Host: "db", Port: 5432, User: "importer",
				Password: "device-password", Database: "smart", Table: "smart_log",
			}, nil
		},
		runSmartctl: func(ctx context.Context, gotDevice string) (string, error) {
			calledSmartctl = true
			if gotDevice != device {
				return "", fmt.Errorf("device = %q, want %q", gotDevice, device)
			}
			if _, ok := ctx.Deadline(); !ok {
				return "", errors.New("smartctl context has no deadline")
			}
			return rawLog, nil
		},
		parseNVMe: func(raw, source string) (model.SmartLog, error) {
			calledParser = true
			if source != device {
				return model.SmartLog{}, fmt.Errorf("parser source = %q, want %q", source, device)
			}
			return parser.ParseNVMe(raw, source)
		},
		openStore: func(ctx context.Context, cfg config.DBConfig, _ *slog.Logger) (store.Store, error) {
			if _, ok := ctx.Deadline(); !ok {
				return nil, errors.New("open context has no deadline")
			}
			return fake, nil
		},
	}

	var stdout, stderr strings.Builder
	args := []string{"-c", "db.yaml", "-d", device, "-log-format", "json"}
	if err := runWithDeps(args, &stdout, &stderr, deps); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if !calledSmartctl || !calledParser {
		t.Fatalf("device pipeline calls = smartctl:%v parser:%v", calledSmartctl, calledParser)
	}
	if !fake.initialized || !fake.upserted || !fake.closed {
		t.Fatalf("store lifecycle = initialized:%v upserted:%v closed:%v", fake.initialized, fake.upserted, fake.closed)
	}
	if !fake.log.SnapshotDate.Equal(time.Date(2026, time.August, 31, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("Local Time fallback date = %v", fake.log.SnapshotDate)
	}
	if fake.log.RawLog != rawLog {
		t.Fatalf("stored raw log = %q, want original input", fake.log.RawLog)
	}
	if !strings.Contains(stderr.String(), `"device":"/dev/nvme0"`) ||
		strings.Contains(stderr.String(), `"file":`) {
		t.Fatalf("device mode input metadata = %q", stderr.String())
	}
	if stdout.Len() == 0 || !strings.Contains(stdout.String(), `serial="...9876"`) {
		t.Fatalf("unexpected device success summary: %q", stdout.String())
	}
	if strings.Contains(stdout.String(), serial) || strings.Contains(stdout.String(), rawLog) ||
		strings.Contains(stdout.String(), "device-password") ||
		strings.Contains(stderr.String(), serial) || strings.Contains(stderr.String(), rawLog) ||
		strings.Contains(stderr.String(), "device-password") {
		t.Fatalf("device output leaked sensitive data: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestRunRejectsBothInputModesBeforeDependencies(t *testing.T) {
	called := false
	deps := importerDeps{
		loadDBConfig: func(string) (config.DBConfig, error) {
			called = true
			return config.DBConfig{}, nil
		},
		parseNVMeFile: func(string) (model.SmartLog, error) {
			called = true
			return model.SmartLog{}, nil
		},
		parseNVMe: func(string, string) (model.SmartLog, error) {
			called = true
			return model.SmartLog{}, nil
		},
		runSmartctl: func(context.Context, string) (string, error) {
			called = true
			return "", nil
		},
		openStore: func(context.Context, config.DBConfig, *slog.Logger) (store.Store, error) {
			called = true
			return nil, nil
		},
	}
	var stdout, stderr strings.Builder
	err := runWithDeps([]string{"-c", "db.yaml", "-f", "report.log", "-d", "/dev/nvme0"}, &stdout, &stderr, deps)
	if err == nil || !strings.Contains(err.Error(), "cannot be used together") {
		t.Fatalf("conflicting input error = %v", err)
	}
	if called || stdout.Len() != 0 || !strings.HasPrefix(stderr.String(), usageText+"\n") ||
		!strings.Contains(stderr.String(), "exactly one input") ||
		strings.Contains(stderr.String(), "import started") {
		t.Fatalf("conflicting input handling = called:%v stdout:%q stderr:%q", called, stdout.String(), stderr.String())
	}
}

func TestRunRejectsNeitherInputModeBeforeDependencies(t *testing.T) {
	called := false
	deps := importerDeps{
		loadDBConfig: func(string) (config.DBConfig, error) {
			called = true
			return config.DBConfig{}, nil
		},
		openStore: func(context.Context, config.DBConfig, *slog.Logger) (store.Store, error) {
			called = true
			return nil, nil
		},
	}
	var stdout, stderr strings.Builder
	err := runWithDeps([]string{"-c", "db.yaml"}, &stdout, &stderr, deps)
	if err == nil || !strings.Contains(err.Error(), "exactly one of -f or -d is required") {
		t.Fatalf("missing input error = %v", err)
	}
	if called || stdout.Len() != 0 || !strings.HasPrefix(stderr.String(), usageText+"\n") ||
		strings.Contains(stderr.String(), "import started") {
		t.Fatalf("missing input handling = called:%v stdout:%q stderr:%q", called, stdout.String(), stderr.String())
	}
}

func TestRunSmartctlFailureSkipsDatabaseAndSuccessOutput(t *testing.T) {
	const password = "device-password"
	calledOpen := false
	deps := importerDeps{
		loadDBConfig: func(string) (config.DBConfig, error) {
			return config.DBConfig{
				Type: "postgres", Host: "db", Port: 5432, User: "importer",
				Password: password, Database: "smart", Table: "smart_log",
			}, nil
		},
		runSmartctl: func(ctx context.Context, device string) (string, error) {
			if _, ok := ctx.Deadline(); !ok {
				return "", errors.New("smartctl context has no deadline")
			}
			return "", fmt.Errorf("smartctl failed using password %s", password)
		},
		parseNVMe: func(string, string) (model.SmartLog, error) {
			return model.SmartLog{}, errors.New("parser must not run")
		},
		openStore: func(context.Context, config.DBConfig, *slog.Logger) (store.Store, error) {
			calledOpen = true
			return nil, nil
		},
	}
	var stdout, stderr strings.Builder
	err := runWithDeps([]string{"-c", "db.yaml", "-d", "/dev/nvme0"}, &stdout, &stderr, deps)
	if err == nil || !strings.Contains(err.Error(), "collect smart log") {
		t.Fatalf("smartctl failure error = %v", err)
	}
	if calledOpen || stdout.Len() != 0 {
		t.Fatalf("smartctl failure continued pipeline: open:%v stdout:%q", calledOpen, stdout.String())
	}
	if strings.Contains(stderr.String(), password) || !strings.Contains(stderr.String(), "[redacted]") {
		t.Fatalf("smartctl failure output redaction = %q", stderr.String())
	}
}
