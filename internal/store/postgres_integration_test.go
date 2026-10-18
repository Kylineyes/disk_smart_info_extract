package store_test

import (
    "bytes"
    "context"
    "crypto/rand"
    "encoding/hex"
    "errors"
    "log/slog"
    "math/big"
    "net"
    "net/url"
    "os"
    "strconv"
    "strings"
    "testing"
    "time"

    "github.com/jackc/pgx/v5"
    "github.com/jackc/pgx/v5/pgconn"

    "smart-log-importer/internal/config"
    "smart-log-importer/internal/model"
    "smart-log-importer/internal/store"
)

const postgresTestConfigEnv = "SMART_IMPORTER_TEST_DB_CONFIG"

func TestPostgresIntegrationRedactsErrorsAndPreservesSQLState(t *testing.T) {
    var logs bytes.Buffer
    logger := slog.New(slog.NewJSONHandler(&logs, nil))
    fixture := newPostgresFixture(t, logger)
    requireSQLSuccess(t, fixture.store.Initialize(fixture.ctx), "initialize snapshot table")

    const serial = "SYNTHETIC-SERIAL-DO-NOT-LOG"
    constraint := pgx.Identifier{serial}.Sanitize()
    _, err := fixture.db.Exec(fixture.ctx,
        "ALTER TABLE "+fixture.table+" ADD CONSTRAINT "+constraint+" CHECK (temperature_c < 0)")
    requireSQLSuccess(t, err, "add test-only constraint")
    temperature := 38
    snapshot := model.SmartLog{
        SnapshotDate: time.Date(2026, time.August, 31, 0, 0, 0, 0, time.UTC),
        Device:       model.DeviceInfo{Model: "Synthetic NVMe", Serial: serial},
        Health:       model.HealthInfo{OverallHealth: "PASSED", TemperatureC: &temperature},
    }
    err = fixture.store.Upsert(fixture.ctx, snapshot)
    var pgError *pgconn.PgError
    if !errors.As(err, &pgError) || pgError.Code != "23514" {
        t.Fatal("Upsert() must preserve PostgreSQL check-constraint error and SQLSTATE 23514")
    }
    if strings.Contains(err.Error(), serial) || strings.Contains(logs.String(), serial) {
        t.Fatal("database error or application log exposed the full serial")
    }
    if !strings.Contains(err.Error(), "23514") {
        t.Fatal("safe error summary must retain the SQLSTATE for diagnosis")
    }
}

func TestPostgresIntegrationSnapshotLifecycle(t *testing.T) {
    fixture := newPostgresFixture(t, nil)
    requireSQLSuccess(t, fixture.store.Initialize(fixture.ctx), "initialize new snapshot table")
    requireSQLSuccess(t, fixture.store.Initialize(fixture.ctx), "repeat schema initialization")

    rows, err := fixture.db.Query(fixture.ctx, `
        SELECT column_name FROM information_schema.columns
        WHERE table_schema = current_schema() AND table_name = $1`, fixture.name)
    requireSQLSuccess(t, err, "query table columns")
    columns, err := pgx.CollectRows(rows, pgx.RowTo[string])
    requireSQLSuccess(t, err, "read table columns")
    if len(columns) != 36 {
        t.Fatalf("column count = %d, want 36", len(columns))
    }
    columnNames := "," + strings.Join(columns, ",") + ","
    for _, removed := range []string{
        "raw_log", "source_file", "local_time_raw",
        "smartctl_version", "pci_vendor_subsystem_id", "no_errors_logged",
    } {
        if strings.Contains(columnNames, ","+removed+",") {
            t.Fatalf("new table still persists removed column %s", removed)
        }
    }
    var uniqueIndexes int
    err = fixture.db.QueryRow(fixture.ctx,
        "SELECT count(*) FROM pg_index WHERE indrelid = $1::regclass AND indisunique",
        fixture.table).Scan(&uniqueIndexes)
    requireSQLSuccess(t, err, "inspect unique indexes")
    if uniqueIndexes != 2 {
        t.Fatalf("unique index count = %d, want primary key and snapshot key", uniqueIndexes)
    }

    const maxCounter = "340282366920938463463374607431768211455"
    counter, ok := new(big.Int).SetString(maxCounter, 10)
    if !ok {
        t.Fatal("invalid synthetic counter")
    }
    temperature, spare := 38, 99
    snapshot := model.SmartLog{
        SnapshotDate: time.Date(2026, time.August, 31, 0, 0, 0, 0, time.UTC),
        Device: model.DeviceInfo{
            Model:                 "合成 NVMe Example",
            Serial:                "SYNTHETIC-INTEGRATION-1234",
            FirmwareVersion:       "1.0",
            TotalNVMCapacityBytes: counter,
        },
        Health: model.HealthInfo{
            OverallHealth:         "PASSED",
            TemperatureC:          &temperature,
            AvailableSparePercent: &spare,
            DataUnitsRead:         counter,
            PowerOnHours:          big.NewInt(42),
        },
        RawLog: "synthetic raw text that must remain only in memory",
    }
    requireSQLSuccess(t, fixture.store.Upsert(fixture.ctx, snapshot), "insert initial snapshot")
    before := readSnapshot(t, fixture, snapshot)
    if before.id <= 0 || before.created.IsZero() || before.updated.IsZero() {
        t.Fatal("database must generate the primary key and timestamps")
    }
    if before.temperature == nil || *before.temperature != 38 || before.spare == nil || *before.spare != 99 {
        t.Fatal("temperature or spare percentage was not persisted exactly")
    }
    if before.readUnits != maxCounter || before.capacity != maxCounter || before.hours != "42" {
        t.Fatal("NUMERIC values were not persisted as exact decimal integers")
    }

    // The sleep runs on the database clock to distinguish consecutive TIMESTAMPTZ values.
    _, err = fixture.db.Exec(fixture.ctx, "SELECT pg_sleep(0.01)")
    requireSQLSuccess(t, err, "advance the database clock")
    snapshot.Device.FirmwareVersion = "2.0"
    snapshot.Health.TemperatureC = nil
    snapshot.Health.AvailableSparePercent = nil
    snapshot.Health.PowerOnHours = big.NewInt(43)
    requireSQLSuccess(t, fixture.store.Upsert(fixture.ctx, snapshot), "update existing snapshot")
    after := readSnapshot(t, fixture, snapshot)
    if after.id != before.id || !after.created.Equal(before.created) || !after.updated.After(before.updated) {
        t.Fatal("UPSERT must preserve id/created_at and refresh updated_at")
    }
    if after.firmware != "2.0" || after.hours != "43" || after.temperature != nil || after.spare != nil {
        t.Fatal("UPSERT must replace input-derived values, including SQL NULLs")
    }
    assertSnapshotCount(t, fixture, 1)

    snapshot.SnapshotDate = snapshot.SnapshotDate.AddDate(0, 0, 1)
    requireSQLSuccess(t, fixture.store.Upsert(fixture.ctx, snapshot), "insert another snapshot date")
    anotherDay := readSnapshot(t, fixture, snapshot)
    if anotherDay.id == before.id {
        t.Fatal("a different snapshot date must produce a new primary key")
    }
    assertSnapshotCount(t, fixture, 2)
    fixture.store.Close()
    fixture.store.Close()
}

type persistedSnapshot struct {
    id          int64
    firmware    string
    temperature *int16
    spare       *int16
    readUnits   string
    capacity    string
    hours       string
    created     time.Time
    updated     time.Time
}

func readSnapshot(t *testing.T, fixture postgresFixture, snapshot model.SmartLog) persistedSnapshot {
    t.Helper()
    var row persistedSnapshot
    err := fixture.db.QueryRow(fixture.ctx, `
        SELECT id, firmware_version, temperature_c, available_spare_percent,
               data_units_read::text, total_nvm_capacity_bytes::text,
               power_on_hours::text, created_at, updated_at
        FROM `+fixture.table+` WHERE snapshot_date = $1 AND model = $2 AND serial = $3`,
        snapshot.SnapshotDate, snapshot.Device.Model, snapshot.Device.Serial,
    ).Scan(&row.id, &row.firmware, &row.temperature, &row.spare,
        &row.readUnits, &row.capacity, &row.hours, &row.created, &row.updated)
    requireSQLSuccess(t, err, "read back persisted snapshot")
    return row
}

func assertSnapshotCount(t *testing.T, fixture postgresFixture, want int) {
    t.Helper()
    var count int
    err := fixture.db.QueryRow(fixture.ctx, "SELECT count(*) FROM "+fixture.table).Scan(&count)
    requireSQLSuccess(t, err, "count snapshot rows")
    if count != want {
        t.Fatalf("snapshot row count = %d, want %d", count, want)
    }
}

type postgresFixture struct {
    ctx   context.Context
    db    *pgx.Conn
    store store.Store
    table string
    name  string
}

func newPostgresFixture(t *testing.T, logger *slog.Logger) postgresFixture {
    t.Helper()
    path := os.Getenv(postgresTestConfigEnv)
    if path == "" {
        t.Skip("set " + postgresTestConfigEnv + " to an isolated PostgreSQL test configuration")
    }
    cfg, err := config.LoadDBConfig(path)
    if err != nil {
        t.Fatalf("load integration configuration: %v", err)
    }
    var suffix [12]byte
    if _, err := rand.Read(suffix[:]); err != nil {
        t.Fatal("generate integration table identifier")
    }
    // Never use the configured table: each test owns exactly one random table.
    cfg.Table = "smart_import_it_" + hex.EncodeToString(suffix[:])
    ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
    t.Cleanup(cancel)

    query := url.Values{}
    if cfg.SSLMode != "" {
        query.Set("sslmode", cfg.SSLMode)
    }
    // pgx accepts Unix socket directories through the host query parameter.
    query.Set("host", cfg.Host)
    query.Set("port", strconv.Itoa(int(cfg.Port)))
    connectionURL := url.URL{
        Scheme:   "postgres",
        User:     url.UserPassword(cfg.User, cfg.Password),
        Host:     net.JoinHostPort("localhost", strconv.Itoa(int(cfg.Port))),
        Path:     "/" + cfg.Database,
        RawQuery: query.Encode(),
    }
    db, err := pgx.Connect(ctx, connectionURL.String())
    requireSQLSuccess(t, err, "connect to isolated integration database")
    table := pgx.Identifier{cfg.Table}.Sanitize()
    t.Cleanup(func() {
        cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
        defer cleanupCancel()
        _, cleanupErr := db.Exec(cleanupCtx, "DROP TABLE IF EXISTS "+table)
        if cleanupErr != nil {
            t.Errorf("drop test-owned table failed (%T)", cleanupErr)
        }
        if closeErr := db.Close(cleanupCtx); closeErr != nil {
            t.Errorf("close integration connection failed (%T)", closeErr)
        }
    })
    st, err := store.Open(ctx, cfg, logger)
    requireSQLSuccess(t, err, "open integration Store")
    t.Cleanup(st.Close)
    return postgresFixture{ctx: ctx, db: db, store: st, table: table, name: cfg.Table}
}

func requireSQLSuccess(t *testing.T, err error, operation string) {
    t.Helper()
    if err != nil {
        // Driver messages may include connection credentials or row values.
        t.Fatalf("%s failed (%T)", operation, err)
    }
}
