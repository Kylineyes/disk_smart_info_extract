package store

import (
	"math/big"
	"strings"
	"testing"
	"time"

	"smart-log-importer/internal/model"
)

func TestPostgresCreateTableSQLContainsSchema(t *testing.T) {
	sql := postgresCreateTableSQL(`"smart_log"`)
	for _, want := range []string{
		`CREATE TABLE IF NOT EXISTS "smart_log"`,
		"id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY",
		"snapshot_date DATE NOT NULL",
		"model VARCHAR(512) NOT NULL",
		"serial VARCHAR(255) NOT NULL",
		"data_units_read NUMERIC(39, 0)",
		"created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP",
		"updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("table SQL does not contain %q", want)
		}
	}
}

func TestPostgresCreateIndexSQL(t *testing.T) {
	sql := postgresCreateIndexSQL(`"smart_log"`, "smart_log")
	want := `CREATE UNIQUE INDEX IF NOT EXISTS "uq_smart_log_date_model_serial" ` +
		`ON "smart_log" (snapshot_date, model, serial)`
	if sql != want {
		t.Errorf("index SQL = %q, want %q", sql, want)
	}
}

func TestPostgresUpsertSQL(t *testing.T) {
	sql := postgresUpsertSQL(`"smart_log"`)
	for _, want := range []string{
		`INSERT INTO "smart_log"`,
		"ON CONFLICT (snapshot_date, model, serial) DO UPDATE SET",
		"updated_at = CURRENT_TIMESTAMP",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("upsert SQL does not contain %q", want)
		}
	}
	if strings.Contains(sql, "created_at = EXCLUDED") {
		t.Error("upsert SQL must not update created_at")
	}
	for index := 1; index <= 33; index++ {
		if !strings.Contains(sql, "$"+itoa(index)) {
			t.Errorf("upsert SQL lacks placeholder $%d", index)
		}
	}
}

func TestPostgresUpsertArguments(t *testing.T) {
	value := 42
	maxCounter, ok := new(big.Int).SetString("340282366920938463463374607431768211455", 10)
	if !ok {
		t.Fatal("unable to construct test counter")
	}
	log := validSmartLog()
	log.Device.TotalNVMCapacityBytes = maxCounter
	log.Health.TemperatureC = &value
	log.Health.DataUnitsRead = maxCounter

	arguments := postgresUpsertArguments(log)
	if got, want := len(arguments), 33; got != want {
		t.Fatalf("argument count = %d, want %d", got, want)
	}
	if got, want := arguments[5], maxCounter.String(); got != want {
		t.Errorf("capacity argument = %#v, want %q", got, want)
	}
	if got, want := arguments[13], value; got != want {
		t.Errorf("temperature argument = %#v, want %d", got, want)
	}
	if got, want := arguments[17], maxCounter.String(); got != want {
		t.Errorf("data units read argument = %#v, want %q", got, want)
	}
	if arguments[14] != nil {
		t.Errorf("missing available spare argument = %#v, want nil", arguments[14])
	}
}

func TestValidateSmartLog(t *testing.T) {
	tests := []struct {
		name string
		edit func(*model.SmartLog)
	}{
		{"missing date", func(log *model.SmartLog) { log.SnapshotDate = time.Time{} }},
		{"missing model", func(log *model.SmartLog) { log.Device.Model = "" }},
		{"long model", func(log *model.SmartLog) { log.Device.Model = strings.Repeat("m", 513) }},
		{"missing serial", func(log *model.SmartLog) { log.Device.Serial = "" }},
		{"long serial", func(log *model.SmartLog) { log.Device.Serial = strings.Repeat("s", 256) }},
		{"missing health", func(log *model.SmartLog) { log.Health.OverallHealth = "" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			log := validSmartLog()
			test.edit(&log)
			if err := validateSmartLog(log); err == nil {
				t.Fatal("validateSmartLog() error = nil")
			}
		})
	}
}

func TestQuoteConnectionValue(t *testing.T) {
	got := quoteConnectionValue(`password with ' quote and \ slash`)
	want := `'password with \' quote and \\ slash'`
	if got != want {
		t.Errorf("quoteConnectionValue() = %q, want %q", got, want)
	}
}

func TestSerialSuffix(t *testing.T) {
	if got, want := serialSuffix("ABCD1234"), "1234"; got != want {
		t.Errorf("serialSuffix() = %q, want %q", got, want)
	}
	if got, want := serialSuffix("ABC"), "ABC"; got != want {
		t.Errorf("serialSuffix() = %q, want %q", got, want)
	}
	if got, want := serialSuffix("序列号1234"), "1234"; got != want {
		t.Errorf("serialSuffix() = %q, want %q", got, want)
	}
}

func TestPostgresCreateIndexSQLBoundsLongIdentifier(t *testing.T) {
	tableName := strings.Repeat("t", 100)
	sql := postgresCreateIndexSQL(`"`+tableName+`"`, tableName)
	indexStart := strings.Index(sql, `"uq_`)
	if indexStart < 0 {
		t.Fatalf("index SQL has no quoted index identifier: %q", sql)
	}
	indexEnd := strings.Index(sql[indexStart+1:], `"`)
	if indexEnd < 0 || indexEnd > 63 {
		t.Fatalf("index identifier exceeds PostgreSQL limit: %q", sql)
	}
	if strings.Contains(sql, "uq_"+tableName) {
		t.Fatalf("index SQL contains unbounded table name: %q", sql)
	}
}

func TestValidateSmartLogRejectsWhitespaceAndCountsRunes(t *testing.T) {
	log := validSmartLog()
	log.Device.Model = "   "
	if err := validateSmartLog(log); err == nil {
		t.Fatal("whitespace model accepted")
	}
	log = validSmartLog()
	log.Device.Model = strings.Repeat("界", 513)
	if err := validateSmartLog(log); err == nil {
		t.Fatal("overlong Unicode model accepted")
	}
}

func validSmartLog() model.SmartLog {
	return model.SmartLog{
		SnapshotDate: time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC),
		Device: model.DeviceInfo{
			Model:  "NVMe Example",
			Serial: "SERIAL1234",
		},
		Health: model.HealthInfo{OverallHealth: "PASSED"},
	}
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	var reversed [20]byte
	index := len(reversed)
	for value > 0 {
		index--
		reversed[index] = byte('0' + value%10)
		value /= 10
	}
	return string(reversed[index:])
}
