// Package parser converts smartctl NVMe text reports into domain values.
package parser

import (
	"bufio"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"smart-log-importer/internal/model"
)

const (
	nvmeHealthMarker = "SMART/Health Information (NVMe Log 0x02)"
	// smartctl output normally contains short lines, but a large limit keeps
	// parsing useful when a future smartctl version emits a long diagnostic.
	maxScannerTokenSize = 16 * 1024 * 1024
)

var (
	filenameDatePattern = regexp.MustCompile(`(?:^|[^0-9])([0-9]{8})(?:[^0-9]|$)`)
	localNumericDate    = regexp.MustCompile(`\b([0-9]{4})[-/]([0-9]{1,2})[-/]([0-9]{1,2})\b`)
	localMonthDate      = regexp.MustCompile(`(?i)\b(January|February|March|April|May|June|July|August|September|October|November|December|Jan|Feb|Mar|Apr|Jun|Jul|Aug|Sep|Oct|Nov|Dec)\s+([0-9]{1,2})(?:,)?\s+([0-9]{4})\b`)
	leadingInteger      = regexp.MustCompile(`^[+-]?[0-9][0-9,]*`)
	validInteger        = regexp.MustCompile(`^[+-]?[0-9]+(?:,[0-9]{3})*$`)
)

// ParseNVMeFile reads a smartctl report from path and parses it.
func ParseNVMeFile(path string) (model.SmartLog, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return model.SmartLog{}, fmt.Errorf("read smartctl log %q: %w", path, err)
	}
	return ParseNVMe(string(data), path)
}

// ParseNVMe parses one NVMe smartctl --all (or -a) text report.
//
// The parser intentionally uses labels rather than line positions. Unknown
// labels are ignored so reports from newer smartctl versions remain usable.
func ParseNVMe(rawLog string, sourceFile string) (model.SmartLog, error) {
	result := model.SmartLog{
		SourceFile: sourceFile,
		RawLog:     rawLog,
	}

	fields := make(map[string]string)
	scanner := bufio.NewScanner(strings.NewReader(rawLog))
	scanner.Buffer(make([]byte, 64*1024), maxScannerTokenSize)
	markerFound := false
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if strings.Contains(trimmed, nvmeHealthMarker) {
			markerFound = true
		}
		if trimmed == "No Errors Logged" {
			result.Health.NoErrorsLogged = true
		}
		if result.SmartctlVersion == "" {
			candidate := strings.TrimSpace(line)
			if strings.HasPrefix(strings.ToLower(candidate), "smartctl ") {
				result.SmartctlVersion = candidate
			}
		}

		colon := strings.IndexByte(line, ':')
		if colon < 0 {
			continue
		}
		label := normalizeLabel(line[:colon])
		if label == "" {
			continue
		}
		if isKnownLabel(label) {
			// Keep the last occurrence. This also makes a complete later section
			// win if a report contains repeated informational headers.
			fields[label] = strings.TrimSpace(line[colon+1:])
		}
	}
	if err := scanner.Err(); err != nil {
		return model.SmartLog{}, fmt.Errorf("scan smartctl log %q: %w", sourceName(sourceFile), err)
	}
	if !markerFound {
		return model.SmartLog{}, fmt.Errorf("unsupported format %q: %s marker not found", sourceName(sourceFile), nvmeHealthMarker)
	}

	if result.Device.Model, _ = requiredString(fields, "model number"); result.Device.Model == "" {
		return model.SmartLog{}, missingFieldError(sourceFile, "Model Number")
	}
	if result.Device.Serial, _ = requiredString(fields, "serial number"); result.Device.Serial == "" {
		return model.SmartLog{}, missingFieldError(sourceFile, "Serial Number")
	}
	if result.Health.OverallHealth, _ = requiredString(fields, "smart overall-health self-assessment test result"); result.Health.OverallHealth == "" {
		return model.SmartLog{}, missingFieldError(sourceFile, "SMART overall-health self-assessment test result")
	}

	result.LocalTimeRaw = fields["local time is"]
	var err error
	result.SnapshotDate, err = snapshotDate(sourceFile, result.LocalTimeRaw)
	if err != nil {
		return model.SmartLog{}, err
	}

	result.Device.FirmwareVersion = fields["firmware version"]
	result.Device.PCIVendorSubsystemID = fields["pci vendor/subsystem id"]
	result.Device.IEEOUIIdentifier = fields["ieee oui identifier"]
	result.Device.NVMVersion = fields["nvme version"]
	result.Device.NamespaceEUI64 = fields["namespace 1 ieee eui-64"]

	if value, ok := fields["total nvm capacity"]; ok && value != "" {
		result.Device.TotalNVMCapacityBytes, err = parseCapacity("Total NVM Capacity", value)
		if err != nil {
			return model.SmartLog{}, err
		}
	}
	if value, ok := fields["number of namespaces"]; ok && value != "" {
		result.Device.NamespaceCount, err = parseInt("Number of Namespaces", value)
		if err != nil {
			return model.SmartLog{}, err
		}
	}
	if value, ok := fields["namespace 1 size/capacity"]; ok && value != "" {
		result.Device.NamespaceCapacityBytes, err = parseCapacity("Namespace 1 Size/Capacity", value)
		if err != nil {
			return model.SmartLog{}, err
		}
	}
	if value, ok := fields["namespace 1 formatted lba size"]; ok && value != "" {
		result.Device.FormattedLBABytes, err = parseInt("Namespace 1 Formatted LBA Size", value)
		if err != nil {
			return model.SmartLog{}, err
		}
	}

	result.Health.CriticalWarning = fields["critical warning"]
	if value, ok := fields["temperature"]; ok && value != "" {
		result.Health.TemperatureC, err = parseIntPointer("Temperature", value)
		if err != nil {
			return model.SmartLog{}, err
		}
	}
	if value, ok := fields["available spare"]; ok && value != "" {
		result.Health.AvailableSparePercent, err = parseIntPointer("Available Spare", value)
		if err != nil {
			return model.SmartLog{}, err
		}
	}
	if value, ok := fields["available spare threshold"]; ok && value != "" {
		result.Health.AvailableSpareThresholdPercent, err = parseIntPointer("Available Spare Threshold", value)
		if err != nil {
			return model.SmartLog{}, err
		}
	}
	if value, ok := fields["percentage used"]; ok && value != "" {
		result.Health.PercentageUsed, err = parseIntPointer("Percentage Used", value)
		if err != nil {
			return model.SmartLog{}, err
		}
	}

	counterFields := []struct {
		label string
		dest  **big.Int
	}{
		{"Data Units Read", &result.Health.DataUnitsRead},
		{"Data Units Written", &result.Health.DataUnitsWritten},
		{"Host Read Commands", &result.Health.HostReadCommands},
		{"Host Write Commands", &result.Health.HostWriteCommands},
		{"Controller Busy Time", &result.Health.ControllerBusyTimeMinutes},
		{"Power Cycles", &result.Health.PowerCycles},
		{"Power On Hours", &result.Health.PowerOnHours},
		{"Unsafe Shutdowns", &result.Health.UnsafeShutdowns},
		{"Media and Data Integrity Errors", &result.Health.MediaDataIntegrityErrors},
		{"Error Information Log Entries", &result.Health.ErrorInformationLogEntries},
		{"Warning Comp. Temperature Time", &result.Health.WarningCompositeTempTime},
		{"Critical Comp. Temperature Time", &result.Health.CriticalCompositeTempTime},
		{"Thermal Temp. 1 Transition Count", &result.Health.ThermalTemp1TransitionCount},
		{"Thermal Temp. 1 Total Time", &result.Health.ThermalTemp1TotalTime},
	}
	for _, field := range counterFields {
		key := normalizeLabel(field.label)
		value, ok := fields[key]
		if !ok || value == "" {
			continue
		}
		*field.dest, err = parseBigInt(field.label, value)
		if err != nil {
			return model.SmartLog{}, err
		}
	}

	for _, field := range []struct {
		key   string
		label string
		dest  **int
	}{
		{"temperature sensor 1", "Temperature Sensor 1", &result.Health.TemperatureSensor1C},
		{"temperature sensor 2", "Temperature Sensor 2", &result.Health.TemperatureSensor2C},
	} {
		value, ok := fields[field.key]
		if !ok || value == "" {
			continue
		}
		*field.dest, err = parseIntPointer(field.label, value)
		if err != nil {
			return model.SmartLog{}, err
		}
	}

	return result, nil
}

func normalizeLabel(label string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(label))), " ")
}

func isKnownLabel(label string) bool {
	switch label {
	case "model number", "serial number", "firmware version", "pci vendor/subsystem id", "ieee oui identifier", "total nvm capacity", "nvme version", "number of namespaces", "namespace 1 size/capacity", "namespace 1 formatted lba size", "namespace 1 ieee eui-64", "local time is", "smart overall-health self-assessment test result", "critical warning", "temperature", "available spare", "available spare threshold", "percentage used", "data units read", "data units written", "host read commands", "host write commands", "controller busy time", "power cycles", "power on hours", "unsafe shutdowns", "media and data integrity errors", "error information log entries", "warning comp. temperature time", "critical comp. temperature time", "temperature sensor 1", "temperature sensor 2", "thermal temp. 1 transition count", "thermal temp. 1 total time":
		return true
	default:
		return false
	}
}

func requiredString(fields map[string]string, key string) (string, bool) {
	value, ok := fields[key]
	return strings.TrimSpace(value), ok && strings.TrimSpace(value) != ""
}

func parseCapacity(label, value string) (*big.Int, error) {
	if bracket := strings.IndexByte(value, '['); bracket >= 0 {
		value = strings.TrimSpace(value[:bracket])
	}
	return parseBigInt(label, value)
}

func parseBigInt(label, value string) (*big.Int, error) {
	token, err := leadingIntegerToken(value)
	if err != nil {
		return nil, fmt.Errorf("parse field %q: %w", label, err)
	}
	cleaned := strings.ReplaceAll(token, ",", "")
	number, ok := new(big.Int).SetString(cleaned, 10)
	if !ok {
		return nil, fmt.Errorf("parse field %q: invalid integer %q", label, token)
	}
	return number, nil
}

func parseInt(label, value string) (int, error) {
	number, err := parseBigInt(label, value)
	if err != nil {
		return 0, err
	}
	if !number.IsInt64() {
		return 0, fmt.Errorf("parse field %q: integer out of range", label)
	}
	converted := number.Int64()
	if strconv.IntSize == 32 && (converted > int64(^uint32(0)>>1) || converted < -int64(^uint32(0)>>1)-1) {
		return 0, fmt.Errorf("parse field %q: integer out of range", label)
	}
	return int(converted), nil
}

func parseIntPointer(label, value string) (*int, error) {
	converted, err := parseInt(label, value)
	if err != nil {
		return nil, err
	}
	return &converted, nil
}

func leadingIntegerToken(value string) (string, error) {
	value = strings.TrimSpace(value)
	match := leadingInteger.FindString(value)
	if match == "" || !validInteger.MatchString(match) {
		return "", fmt.Errorf("invalid integer value %q", value)
	}
	if len(value) > len(match) {
		next := value[len(match)]
		// smartctl puts units and display values after a space (or starts a
		// bracketed display value). Reject an attached suffix instead of
		// silently truncating values such as "0x10" or "123oops".
		if next != '[' && next != '%' && (next < '\t' || next > '\r') && next != ' ' {
			return "", fmt.Errorf("invalid integer value %q", value)
		}
	}
	return match, nil
}

func snapshotDate(sourceFile, localTimeRaw string) (time.Time, error) {
	base := filepath.Base(sourceFile)
	for _, match := range filenameDatePattern.FindAllStringSubmatch(base, -1) {
		if date, err := time.Parse("20060102", match[1]); err == nil {
			return date.UTC(), nil
		}
	}
	if localTimeRaw != "" {
		if date, ok := parseLocalDate(localTimeRaw); ok {
			return date, nil
		}
	}
	return time.Time{}, fmt.Errorf("missing or invalid snapshot date in %q (filename date and Local Time is unavailable)", sourceName(sourceFile))
}

func parseLocalDate(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	layouts := []string{
		"Mon Jan 2 15:04:05 2006 MST",
		"Mon Jan 02 15:04:05 2006 MST",
		"Mon Jan 2 15:04:05 2006",
		"Mon Jan 02 15:04:05 2006",
		"Jan 2 15:04:05 2006 MST",
		"Jan 02 15:04:05 2006 MST",
		"2006-01-02 15:04:05 MST",
		"2006-01-02 15:04:05 -0700 MST",
		"2006-01-02T15:04:05Z07:00",
		"2006-01-02",
	}
	for _, layout := range layouts {
		if parsed, err := time.Parse(layout, value); err == nil {
			return time.Date(parsed.Year(), parsed.Month(), parsed.Day(), 0, 0, 0, 0, time.UTC), true
		}
	}
	if match := localNumericDate.FindStringSubmatch(value); len(match) == 4 {
		if parsed, err := time.Parse("2006-1-2", fmt.Sprintf("%s-%s-%s", match[1], match[2], match[3])); err == nil {
			return parsed.UTC(), true
		}
	}
	if match := localMonthDate.FindStringSubmatch(value); len(match) == 4 {
		if parsed, err := time.Parse("Jan 2 2006", fmt.Sprintf("%s %s %s", match[1][:3], match[2], match[3])); err == nil {
			return parsed.UTC(), true
		}
	}
	return time.Time{}, false
}

func sourceName(sourceFile string) string {
	if strings.TrimSpace(sourceFile) == "" {
		return "<input>"
	}
	return sourceFile
}

func missingFieldError(sourceFile, field string) error {
	return fmt.Errorf("missing required field %q in %q", field, sourceName(sourceFile))
}
