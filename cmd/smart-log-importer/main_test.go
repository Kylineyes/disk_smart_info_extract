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
	fake := new(fakeStore)
	deps := importerDeps{
		loadDBConfig: func(string) (config.DBConfig, error) {
			return config.DBConfig{Type: "postgres", Host: "db", Port: 5432, User: "importer", Password: password, Database: "smart", Table: "smart_log"}, nil
		},
		parseNVMeFile: func(path string) (model.SmartLog, error) {
			return model.SmartLog{
				SnapshotDate: time.Date(2026, time.August, 31, 0, 0, 0, 0, time.UTC),
				SourceFile:   path,
				Device:       model.DeviceInfo{Model: "Example NVMe 4TB", Serial: serial},
				Health:       model.HealthInfo{OverallHealth: "PASSED"},
				RawLog:       "raw SMART report",
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
	if err := runWithDeps([]string{"-c", "db.yaml", "-f", "report.log", "-log-format", "json"}, &stdout, &stderr, deps); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if !fake.initialized || !fake.upserted || !fake.closed {
		t.Fatalf("store lifecycle = initialized:%v upserted:%v closed:%v", fake.initialized, fake.upserted, fake.closed)
	}
	if got := stdout.String(); !strings.Contains(got, `imported smart log: date=2026-08-31`) || !strings.Contains(got, `serial="...3456"`) {
		t.Fatalf("unexpected stdout: %q", got)
	}
	if strings.Contains(stdout.String(), serial) || strings.Contains(stdout.String(), password) {
		t.Fatalf("sensitive value in stdout: %q", stdout.String())
	}
	if strings.Contains(stderr.String(), serial) || strings.Contains(stderr.String(), password) {
		t.Fatalf("sensitive value in stderr: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), `"msg":"import started"`) || !strings.Contains(stderr.String(), `"msg":"smart log upserted"`) {
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
			return config.DBConfig{Type: "postgres", Host: "db", Port: 5432, User: "importer", Password: password, Database: "smart", Table: "smart_log"}, nil
		},
		parseNVMeFile: func(path string) (model.SmartLog, error) {
			return model.SmartLog{
				SnapshotDate: time.Date(2026, time.August, 31, 0, 0, 0, 0, time.UTC),
				SourceFile:   path,
				Device:       model.DeviceInfo{Model: "Example NVMe 4TB", Serial: serial},
				Health:       model.HealthInfo{OverallHealth: "PASSED"},
				RawLog:       "raw SMART report",
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
	if !strings.Contains(stderr.String(), "import failed") || !strings.Contains(stderr.String(), "configuration unavailable") {
		t.Fatalf("missing failure diagnostic: %q", stderr.String())
	}
}
