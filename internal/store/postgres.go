package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"smart-log-importer/internal/config"
	"smart-log-importer/internal/model"
)

const storeComponent = "store"

type postgresStore struct {
	pool      *pgxpool.Pool
	table     string
	logger    *slog.Logger
	ddl       string
	indexDDL  string
	upsertSQL string
	closeOnce sync.Once
}

func openPostgres(ctx context.Context, cfg config.DBConfig, logger *slog.Logger) (Store, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid database configuration: %w", err)
	}
	if ctx == nil {
		return nil, errors.New("PostgreSQL connection context is required")
	}
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	// Build a keyword/value connection string so pgx applies its defaults and
	// preserves sslmode exactly when the caller supplied one. Values are quoted
	// by pgx's parser and never included in application logs or returned errors.
	connString := fmt.Sprintf(
		"host=%s port=%d user=%s password=%s dbname=%s",
		quoteConnectionValue(cfg.Host),
		cfg.Port,
		quoteConnectionValue(cfg.User),
		quoteConnectionValue(cfg.Password),
		quoteConnectionValue(cfg.Database),
	)
	if cfg.SSLMode != "" {
		connString += " sslmode=" + quoteConnectionValue(cfg.SSLMode)
	}
	poolConfig, err := pgxpool.ParseConfig(connString)
	if err != nil {
		return nil, fmt.Errorf("prepare PostgreSQL connection configuration: %w", sanitizeConnectionError(err, cfg))
	}

	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("open PostgreSQL connection pool: %w", sanitizeConnectionError(err, cfg))
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping PostgreSQL database: %w", sanitizeConnectionError(err, cfg))
	}

	quotedTable := pgx.Identifier{cfg.Table}.Sanitize()
	store := &postgresStore{
		pool:      pool,
		table:     cfg.Table,
		logger:    logger,
		ddl:       postgresCreateTableSQL(quotedTable),
		indexDDL:  postgresCreateIndexSQL(quotedTable, cfg.Table),
		upsertSQL: postgresUpsertSQL(quotedTable),
	}
	logger.Info("PostgreSQL connection established", "component", storeComponent, "table", cfg.Table)
	return store, nil
}

// Initialize creates the SMART snapshot table and its conflict-key index. Both
// statements are idempotent and may be called more than once.
func (s *postgresStore) Initialize(ctx context.Context) error {
	started := time.Now()
	if _, err := s.pool.Exec(ctx, s.ddl); err != nil {
		s.logger.Error("PostgreSQL schema initialization failed", "component", storeComponent, "table", s.table, "error", err)
		return fmt.Errorf("create PostgreSQL table: %w", err)
	}
	if _, err := s.pool.Exec(ctx, s.indexDDL); err != nil {
		s.logger.Error("PostgreSQL index initialization failed", "component", storeComponent, "table", s.table, "error", err)
		return fmt.Errorf("create PostgreSQL index: %w", err)
	}
	s.logger.Info(
		"PostgreSQL schema initialized",
		"component", storeComponent,
		"table", s.table,
		"duration", time.Since(started),
	)
	return nil
}

// Upsert writes one SMART snapshot, replacing input-derived fields if its date,
// model, and serial already exist. The database assigns created_at only for a
// newly inserted row.
func (s *postgresStore) Upsert(ctx context.Context, log model.SmartLog) error {
	if err := validateSmartLog(log); err != nil {
		return err
	}

	started := time.Now()
	_, err := s.pool.Exec(ctx, s.upsertSQL, postgresUpsertArguments(log)...)
	if err != nil {
		s.logger.Error("postgres upsert failed", "component", storeComponent, "table", s.table, "error", err)
		return fmt.Errorf("upsert PostgreSQL SMART snapshot: %w", err)
	}
	s.logger.Info(
		"PostgreSQL SMART snapshot upserted",
		"component", storeComponent,
		"table", s.table,
		"snapshot_date", log.SnapshotDate.Format("2006-01-02"),
		"model", log.Device.Model,
		"serial_suffix", serialSuffix(log.Device.Serial),
		"duration", time.Since(started),
	)
	return nil
}

// Close releases the PostgreSQL connection pool. It is safe to call repeatedly.
func (s *postgresStore) Close() {
	s.closeOnce.Do(func() {
		if s.pool != nil {
			s.pool.Close()
		}
	})
}

func validateSmartLog(log model.SmartLog) error {
	if log.SnapshotDate.IsZero() {
		return errors.New("SMART snapshot date is required")
	}
	if strings.TrimSpace(log.Device.Model) == "" {
		return errors.New("SMART device model is required")
	}
	if utf8.RuneCountInString(log.Device.Model) > 512 {
		return errors.New("SMART device model exceeds 512 characters")
	}
	if strings.TrimSpace(log.Device.Serial) == "" {
		return errors.New("SMART device serial is required")
	}
	if utf8.RuneCountInString(log.Device.Serial) > 255 {
		return errors.New("SMART device serial exceeds 255 characters")
	}
	if strings.TrimSpace(log.Health.OverallHealth) == "" {
		return errors.New("SMART overall health is required")
	}
	return nil
}

func postgresUpsertArguments(log model.SmartLog) []any {
	return []any{
		log.SnapshotDate,
		log.Device.Model,
		log.Device.Serial,
		nullIfEmpty(log.Device.FirmwareVersion),
		nullIfEmpty(log.Device.IEEOUIIdentifier),
		bigIntString(log.Device.TotalNVMCapacityBytes),
		nullIfEmpty(log.Device.NVMVersion),
		nullIfZero(log.Device.NamespaceCount),
		bigIntString(log.Device.NamespaceCapacityBytes),
		nullIfZero(log.Device.FormattedLBABytes),
		nullIfEmpty(log.Device.NamespaceEUI64),
		log.Health.OverallHealth,
		nullIfEmpty(log.Health.CriticalWarning),
		intPtrValue(log.Health.TemperatureC),
		intPtrValue(log.Health.AvailableSparePercent),
		intPtrValue(log.Health.AvailableSpareThresholdPercent),
		intPtrValue(log.Health.PercentageUsed),
		bigIntString(log.Health.DataUnitsRead),
		bigIntString(log.Health.DataUnitsWritten),
		bigIntString(log.Health.HostReadCommands),
		bigIntString(log.Health.HostWriteCommands),
		bigIntString(log.Health.ControllerBusyTimeMinutes),
		bigIntString(log.Health.PowerCycles),
		bigIntString(log.Health.PowerOnHours),
		bigIntString(log.Health.UnsafeShutdowns),
		bigIntString(log.Health.MediaDataIntegrityErrors),
		bigIntString(log.Health.ErrorInformationLogEntries),
		bigIntString(log.Health.WarningCompositeTempTime),
		bigIntString(log.Health.CriticalCompositeTempTime),
		intPtrValue(log.Health.TemperatureSensor1C),
		intPtrValue(log.Health.TemperatureSensor2C),
		bigIntString(log.Health.ThermalTemp1TransitionCount),
		bigIntString(log.Health.ThermalTemp1TotalTime),
	}
}

func postgresCreateTableSQL(table string) string {
	return fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY, -- Database-generated snapshot identifier.
    snapshot_date DATE NOT NULL, -- Calendar date represented by this SMART snapshot.

    model VARCHAR(512) NOT NULL, -- NVMe Model Number.
    serial VARCHAR(255) NOT NULL, -- NVMe Serial Number.
    firmware_version TEXT, -- NVMe Firmware Version.
    ieee_oui_identifier TEXT, -- IEEE OUI identifier for the controller vendor.
    total_nvm_capacity_bytes NUMERIC(39, 0), -- Total NVM capacity in bytes.
    nvm_version TEXT, -- NVMe specification version.
    namespace_count INTEGER, -- Number of namespaces reported by the controller.
    namespace_capacity_bytes NUMERIC(39, 0), -- Namespace 1 capacity in bytes.
    formatted_lba_bytes INTEGER, -- Namespace 1 formatted logical block size in bytes.
    namespace_eui64 TEXT, -- Namespace 1 IEEE EUI-64 identifier.

    overall_health TEXT NOT NULL, -- SMART overall-health self-assessment result.
    critical_warning TEXT, -- NVMe Critical Warning bitmask as reported.
    temperature_c SMALLINT, -- Composite temperature in degrees Celsius.
    available_spare_percent SMALLINT, -- Percentage of remaining available spare.
    available_spare_threshold_percent SMALLINT, -- Available-spare warning threshold percentage.
    percentage_used SMALLINT, -- Estimated device lifetime percentage used.
    data_units_read NUMERIC(39, 0), -- Cumulative NVMe data units read (512,000 bytes each).
    data_units_written NUMERIC(39, 0), -- Cumulative NVMe data units written (512,000 bytes each).
    host_read_commands NUMERIC(39, 0), -- Cumulative host read command count.
    host_write_commands NUMERIC(39, 0), -- Cumulative host write command count.
    controller_busy_time_minutes NUMERIC(39, 0), -- Cumulative controller busy time in minutes.
    power_cycles NUMERIC(39, 0), -- Cumulative power-cycle count.
    power_on_hours NUMERIC(39, 0), -- Cumulative power-on time in hours.
    unsafe_shutdowns NUMERIC(39, 0), -- Cumulative unsafe shutdown count.
    media_data_integrity_errors NUMERIC(39, 0), -- Cumulative media/data integrity error count.
    error_information_log_entries NUMERIC(39, 0), -- Cumulative error-information log entry count.
    warning_composite_temp_time NUMERIC(39, 0), -- Time in the warning composite-temperature range (controller units).
    critical_composite_temp_time NUMERIC(39, 0), -- Time in the critical composite-temperature range (controller units).
    temperature_sensor_1_c SMALLINT, -- Temperature sensor 1 reading in degrees Celsius.
    temperature_sensor_2_c SMALLINT, -- Temperature sensor 2 reading in degrees Celsius.
    thermal_temp_1_transition_count NUMERIC(39, 0), -- Cumulative transitions into thermal-temperature threshold 1.
    thermal_temp_1_total_time NUMERIC(39, 0), -- Cumulative time at thermal-temperature threshold 1 (controller units).

    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP, -- Time this row was first inserted.
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP -- Time this row was last updated.
)`, table)
}

func postgresCreateIndexSQL(table, tableName string) string {
	// PostgreSQL limits identifiers to 63 bytes. Keep the readable name for
	// ordinary tables, and include a digest when a valid configured table name
	// would otherwise make the index name exceed that limit. The digest avoids
	// collisions between long table names sharing the same prefix.
	indexName := "uq_" + tableName + "_date_model_serial"
	if len(indexName) > 63 {
		digest := sha256.Sum256([]byte(tableName))
		indexName = "uq_" + tableName[:27] + "_" + hex.EncodeToString(digest[:])[:16]
	}
	return fmt.Sprintf(
		"CREATE UNIQUE INDEX IF NOT EXISTS %s ON %s (snapshot_date, model, serial)",
		pgx.Identifier{indexName}.Sanitize(),
		table,
	)
}

func postgresUpsertSQL(table string) string {
	return fmt.Sprintf(`INSERT INTO %s (
    snapshot_date, model, serial, firmware_version, ieee_oui_identifier,
    total_nvm_capacity_bytes, nvm_version, namespace_count,
    namespace_capacity_bytes, formatted_lba_bytes, namespace_eui64,
    overall_health, critical_warning, temperature_c, available_spare_percent,
    available_spare_threshold_percent, percentage_used, data_units_read,
    data_units_written, host_read_commands, host_write_commands,
    controller_busy_time_minutes, power_cycles, power_on_hours, unsafe_shutdowns,
    media_data_integrity_errors, error_information_log_entries,
    warning_composite_temp_time, critical_composite_temp_time,
    temperature_sensor_1_c, temperature_sensor_2_c,
    thermal_temp_1_transition_count, thermal_temp_1_total_time
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15,
    $16, $17, $18, $19, $20, $21, $22, $23, $24, $25, $26, $27, $28, $29,
    $30, $31, $32, $33
)
ON CONFLICT (snapshot_date, model, serial) DO UPDATE SET
    firmware_version = EXCLUDED.firmware_version,
    ieee_oui_identifier = EXCLUDED.ieee_oui_identifier,
    total_nvm_capacity_bytes = EXCLUDED.total_nvm_capacity_bytes,
    nvm_version = EXCLUDED.nvm_version,
    namespace_count = EXCLUDED.namespace_count,
    namespace_capacity_bytes = EXCLUDED.namespace_capacity_bytes,
    formatted_lba_bytes = EXCLUDED.formatted_lba_bytes,
    namespace_eui64 = EXCLUDED.namespace_eui64,
    overall_health = EXCLUDED.overall_health,
    critical_warning = EXCLUDED.critical_warning,
    temperature_c = EXCLUDED.temperature_c,
    available_spare_percent = EXCLUDED.available_spare_percent,
    available_spare_threshold_percent = EXCLUDED.available_spare_threshold_percent,
    percentage_used = EXCLUDED.percentage_used,
    data_units_read = EXCLUDED.data_units_read,
    data_units_written = EXCLUDED.data_units_written,
    host_read_commands = EXCLUDED.host_read_commands,
    host_write_commands = EXCLUDED.host_write_commands,
    controller_busy_time_minutes = EXCLUDED.controller_busy_time_minutes,
    power_cycles = EXCLUDED.power_cycles,
    power_on_hours = EXCLUDED.power_on_hours,
    unsafe_shutdowns = EXCLUDED.unsafe_shutdowns,
    media_data_integrity_errors = EXCLUDED.media_data_integrity_errors,
    error_information_log_entries = EXCLUDED.error_information_log_entries,
    warning_composite_temp_time = EXCLUDED.warning_composite_temp_time,
    critical_composite_temp_time = EXCLUDED.critical_composite_temp_time,
    temperature_sensor_1_c = EXCLUDED.temperature_sensor_1_c,
    temperature_sensor_2_c = EXCLUDED.temperature_sensor_2_c,
    thermal_temp_1_transition_count = EXCLUDED.thermal_temp_1_transition_count,
    thermal_temp_1_total_time = EXCLUDED.thermal_temp_1_total_time,
    updated_at = CURRENT_TIMESTAMP`, table)
}

func bigIntString(value *big.Int) any {
	if value == nil {
		return nil
	}
	return value.String()
}

func nullIfEmpty(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullIfZero(value int) any {
	if value == 0 {
		return nil
	}
	return value
}

func intPtrValue(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}

func serialSuffix(serial string) string {
	runes := []rune(serial)
	if len(runes) <= 4 {
		return serial
	}
	return string(runes[len(runes)-4:])
}

func sanitizeConnectionError(err error, cfg config.DBConfig) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	for _, secret := range []string{cfg.Password, cfg.User, cfg.Host, cfg.Database} {
		if secret != "" {
			message = strings.ReplaceAll(message, secret, "[redacted]")
		}
	}
	return errors.New(message)
}

func quoteConnectionValue(value string) string {
	// PostgreSQL keyword/value syntax uses backslash escaping in single-quoted
	// values. This keeps all configuration values data rather than connection
	// string syntax.
	return "'" + strings.ReplaceAll(strings.ReplaceAll(value, `\`, `\\`), `'`, `\'`) + "'"
}
