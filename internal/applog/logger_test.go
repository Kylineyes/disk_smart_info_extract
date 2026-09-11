package applog

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestNewRejectsInvalidLevel(t *testing.T) {
	if _, err := New(&bytes.Buffer{}, "verbose", "text"); err == nil {
		t.Fatal("New accepted an invalid log level")
	}
}

func TestNewRejectsInvalidFormat(t *testing.T) {
	if _, err := New(&bytes.Buffer{}, "info", "yaml"); err == nil {
		t.Fatal("New accepted an invalid log format")
	}
}

func TestNewJSONOutputIsValidJSON(t *testing.T) {
	var output bytes.Buffer
	logger, err := New(&output, "info", "json")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	logger.Info("import started", "component", "import")

	var record map[string]any
	if err := json.Unmarshal(output.Bytes(), &record); err != nil {
		t.Fatalf("JSON log is invalid: %v; output=%q", err, output.String())
	}
	if record["msg"] != "import started" || record["component"] != "import" {
		t.Fatalf("unexpected JSON log record: %#v", record)
	}
}

func TestNewTextOutput(t *testing.T) {
	var output bytes.Buffer
	logger, err := New(&output, "info", "text")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	logger.Info("import started", "component", "import")
	if !strings.Contains(output.String(), "level=INFO") ||
		!strings.Contains(output.String(), "msg=\"import started\"") ||
		!strings.Contains(output.String(), "component=import") {
		t.Fatalf("unexpected text log output: %q", output.String())
	}
}

func TestNewHonorsLogLevel(t *testing.T) {
	var output bytes.Buffer
	logger, err := New(&output, "warn", "text")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	logger.Info("not emitted")
	logger.Warn("emitted")
	if strings.Contains(output.String(), "not emitted") || !strings.Contains(output.String(), "emitted") {
		t.Fatalf("logger did not honor level: %q", output.String())
	}
}

func TestSerialSuffix(t *testing.T) {
	for _, test := range []struct {
		serial string
		want   string
	}{
		{serial: "", want: ""},
		{serial: "123", want: "123"},
		{serial: "1234", want: "1234"},
		{serial: "SN-123456", want: "3456"},
		{serial: "序列号1234", want: "1234"},
	} {
		if got := SerialSuffix(test.serial); got != test.want {
			t.Errorf("SerialSuffix(%q) = %q, want %q", test.serial, got, test.want)
		}
	}
}

func TestNewDefaults(t *testing.T) {
	var output bytes.Buffer
	logger, err := New(&output, "", "")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if !logger.Enabled(t.Context(), slog.LevelInfo) {
		t.Fatal("default logger does not enable info")
	}
	logger.Info("default")
	if !strings.Contains(output.String(), "msg=default") {
		t.Fatalf("unexpected default output: %q", output.String())
	}
}
