package parser

import (
	"math/big"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseNVMeHealthy(t *testing.T) {
	path := filepath.Join("..", "..", "testdata", "nvme_healthy_20260831.log")
	got, err := ParseNVMeFile(path)
	if err != nil {
		t.Fatalf("ParseNVMeFile() error = %v", err)
	}
	if got.SnapshotDate != time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC) {
		t.Fatalf("SnapshotDate = %v", got.SnapshotDate)
	}
	if got.SourceFile != path || got.SmartctlVersion == "" || got.RawLog == "" {
		t.Fatalf("metadata not retained: %#v", got)
	}
	if got.Device.Model != "Example NVMe SSD 4TB" || got.Device.Serial != "SN-REDACTED-1356" {
		t.Fatalf("identity = %#v", got.Device)
	}
	if got.Device.TotalNVMCapacityBytes.Cmp(big.NewInt(4000787030016)) != 0 {
		t.Fatalf("capacity = %s", got.Device.TotalNVMCapacityBytes)
	}
	if got.Device.NamespaceCapacityBytes.Cmp(big.NewInt(4000787030016)) != 0 || got.Device.FormattedLBABytes != 512 {
		t.Fatalf("namespace = %#v", got.Device)
	}
	if got.Health.OverallHealth != "PASSED" || got.Health.CriticalWarning != "0x00" || !got.Health.NoErrorsLogged {
		t.Fatalf("health summary = %#v", got.Health)
	}
	if got.Health.TemperatureC == nil || *got.Health.TemperatureC != 38 || got.Health.PercentageUsed == nil || *got.Health.PercentageUsed != 1 {
		t.Fatalf("health values = %#v", got.Health)
	}
	if got.Health.DataUnitsRead.Cmp(big.NewInt(1234567)) != 0 || got.Health.HostWriteCommands.Cmp(big.NewInt(87654321)) != 0 {
		t.Fatalf("counters = %#v", got.Health)
	}
}

func TestParseNVMeFilenameDateAndFallback(t *testing.T) {
	raw := fixtureRaw()
	got, err := ParseNVMe(raw, "/tmp/report-20260831.any")
	if err != nil {
		t.Fatal(err)
	}
	if got.SnapshotDate.Day() != 31 || got.SnapshotDate.Month() != time.August || got.SnapshotDate.Year() != 2026 {
		t.Fatalf("filename date = %v", got.SnapshotDate)
	}
	got, err = ParseNVMe(raw, "/tmp/report.smart_log")
	if err != nil {
		t.Fatal(err)
	}
	if !got.SnapshotDate.Equal(time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("fallback date = %v", got.SnapshotDate)
	}
}

func TestParseNVMeErrors(t *testing.T) {
	if _, err := ParseNVMe("Model Number: x\n", "ata.log"); err == nil || !strings.Contains(err.Error(), "unsupported format") {
		t.Fatalf("unsupported error = %v", err)
	}
	raw := fixtureRaw()
	for _, field := range []string{"Model Number", "Serial Number", "SMART overall-health self-assessment test result"} {
		without := removeLabel(raw, field)
		_, err := ParseNVMe(without, "20260831.log")
		if err == nil || !strings.Contains(err.Error(), field) {
			t.Errorf("missing %s error = %v", field, err)
		}
	}
	bad := strings.Replace(raw, "Power Cycles: 1", "Power Cycles: nonsense", 1)
	if _, err := ParseNVMe(bad, "20260831.log"); err == nil || !strings.Contains(err.Error(), "Power Cycles") {
		t.Errorf("invalid number error = %v", err)
	}
}

func TestParseNVMeOptionalSensors(t *testing.T) {
	raw := fixtureRaw()
	raw = removeLabel(removeLabel(raw, "Temperature Sensor 1"), "Temperature Sensor 2")
	got, err := ParseNVMe(raw, "20260831.log")
	if err != nil {
		t.Fatal(err)
	}
	if got.Health.TemperatureSensor1C != nil || got.Health.TemperatureSensor2C != nil {
		t.Fatalf("optional sensors = %#v", got.Health)
	}
}

func fixtureRaw() string {
	return "smartctl 7.5\n" +
		"Model Number: Demo\nSerial Number: Demo-1234\nFirmware Version: 1\n" +
		"Local Time is: Mon Aug 31 08:00:01 2026 CST\n" +
		"SMART overall-health self-assessment test result: PASSED\n" +
		nvmeHealthMarker + "\n" +
		"Power Cycles: 1\nTemperature: 30 Celsius\nTemperature Sensor 1: 30 Celsius\nTemperature Sensor 2: 31 Celsius\n"
}

func removeLabel(raw, label string) string {
	lines := strings.Split(raw, "\n")
	filtered := lines[:0]
	for _, line := range lines {
		if !strings.HasPrefix(line, label+":") {
			filtered = append(filtered, line)
		}
	}
	return strings.Join(filtered, "\n")
}
