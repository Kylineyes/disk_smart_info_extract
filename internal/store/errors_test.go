package store_test

import (
    "context"
    "errors"
    "strings"
    "testing"

    "github.com/jackc/pgx/v5/pgconn"

    "smart-log-importer/internal/config"
    "smart-log-importer/internal/store"
)

func TestOpenConfigurationErrorDoesNotRenderConnectionString(t *testing.T) {
    cfg := config.DBConfig{
        Type:     "postgres",
        Host:     "127.0.0.1",
        Port:     5432,
        User:     "integration_writer",
        Password: "synthetic-'quoted-password",
        Database: "smart_importer_test",
        Table:    "smart_snapshots",
        SSLMode:  "invalid-tls-mode",
    }
    st, err := store.Open(context.Background(), cfg, nil)
    if st != nil {
        st.Close()
    }
    var parseError *pgconn.ParseConfigError
    if !errors.As(err, &parseError) {
        t.Fatal("Open() must preserve the PostgreSQL configuration error")
    }
    for _, forbidden := range []string{"host=", "password=", "postgres://", cfg.Password} {
        if strings.Contains(err.Error(), forbidden) {
            t.Fatal("Open() configuration error rendered a connection string or password")
        }
    }
}
func TestOpenPreservesCanceledContextWithoutExposingCredentials(t *testing.T) {
    ctx, cancel := context.WithCancel(context.Background())
    cancel()
    cfg := config.DBConfig{
        Type:     "postgres",
        Host:     "127.0.0.1",
        Port:     5432,
        User:     "integration_writer",
        Password: "test-only-password",
        Database: "smart_importer_test",
        Table:    "smart_snapshots",
        SSLMode:  "disable",
    }
    st, err := store.Open(ctx, cfg, nil)
    if st != nil {
        st.Close()
    }
    if !errors.Is(err, context.Canceled) {
        t.Fatalf("Open() must preserve context.Canceled, got %v", err)
    }
    for _, secret := range []string{cfg.Password, "password=", "postgres://"} {
        if strings.Contains(err.Error(), secret) {
            t.Fatalf("Open() error contains credentials or a connection string")
        }
    }
}
