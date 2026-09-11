package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadDBConfig(t *testing.T) {
	password := "do-not-leak-this-password"
	path := writeConfig(t, ""+
		"type: postgres\n"+
		"host: db.example.test\n"+
		"port: 5432\n"+
		"user: smart_writer\n"+
		"password: "+password+"\n"+
		"database: smart\n"+
		"table: smart_log\n"+
		"sslmode: require\n")

	cfg, err := LoadDBConfig(path)
	if err != nil {
		t.Fatalf("LoadDBConfig() error = %v", err)
	}
	if cfg.Host != "db.example.test" || cfg.Port != 5432 || cfg.Table != "smart_log" || cfg.SSLMode != "require" {
		t.Fatalf("LoadDBConfig() = %#v", cfg)
	}
}

func TestLoadDBConfigRejectsInvalidYAMLWithoutLeakingPassword(t *testing.T) {
	password := "do-not-leak-this-password"
	path := writeConfig(t, "password: "+password+"\ninvalid: [\n")

	_, err := LoadDBConfig(path)
	if err == nil {
		t.Fatal("LoadDBConfig() error = nil")
	}
	if strings.Contains(err.Error(), password) {
		t.Fatalf("error leaked password: %v", err)
	}
}

func TestLoadDBConfigRejectsUnknownField(t *testing.T) {
	path := writeConfig(t, validConfigYAML("extra: unexpected\n"))

	_, err := LoadDBConfig(path)
	if err == nil {
		t.Fatal("LoadDBConfig() error = nil")
	}
	if got, want := err.Error(), "decode database configuration: invalid YAML"; got != want {
		t.Errorf("error = %q, want %q", got, want)
	}
}

func TestDBConfigValidate(t *testing.T) {
	password := "do-not-leak-this-password"
	valid := DBConfig{
		Type:     "postgres",
		Host:     "db.example.test",
		Port:     5432,
		User:     "smart_writer",
		Password: password,
		Database: "smart",
		Table:    "smart_log",
	}

	tests := []struct {
		name string
		edit func(*DBConfig)
	}{
		{"non-postgres type", func(cfg *DBConfig) { cfg.Type = "mysql" }},
		{"missing host", func(cfg *DBConfig) { cfg.Host = "" }},
		{"missing user", func(cfg *DBConfig) { cfg.User = "" }},
		{"missing password", func(cfg *DBConfig) { cfg.Password = "" }},
		{"missing database", func(cfg *DBConfig) { cfg.Database = "" }},
		{"missing table", func(cfg *DBConfig) { cfg.Table = "" }},
		{"invalid table punctuation", func(cfg *DBConfig) { cfg.Table = "smart-log" }},
		{"invalid table SQL", func(cfg *DBConfig) { cfg.Table = "smart_log; DROP TABLE smart_log" }},
		{"invalid table placeholder", func(cfg *DBConfig) { cfg.Table = "$1" }},
		{"invalid table starts digit", func(cfg *DBConfig) { cfg.Table = "1smart_log" }},
		{"table longer than PostgreSQL identifier limit", func(cfg *DBConfig) { cfg.Table = strings.Repeat("a", 64) }},
		{"port zero", func(cfg *DBConfig) { cfg.Port = 0 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := valid
			test.edit(&cfg)
			err := cfg.Validate()
			if err == nil {
				t.Fatal("Validate() error = nil")
			}
			if strings.Contains(err.Error(), password) {
				t.Fatalf("error leaked password: %v", err)
			}
		})
	}
}

func TestIsSimpleIdentifier(t *testing.T) {
	for _, name := range []string{"smart_log", "SmartLog", "_smart_log", "smart_log_2026"} {
		if !IsSimpleIdentifier(name) {
			t.Errorf("IsSimpleIdentifier(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"", "1smart_log", "smart-log", "public.smart_log", "$1", "smart log"} {
		if IsSimpleIdentifier(name) {
			t.Errorf("IsSimpleIdentifier(%q) = true, want false", name)
		}
	}
}

func writeConfig(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "db.yaml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func validConfigYAML(extra string) string {
	return "" +
		"type: postgres\n" +
		"host: db.example.test\n" +
		"port: 5432\n" +
		"user: smart_writer\n" +
		"password: do-not-leak-this-password\n" +
		"database: smart\n" +
		"table: smart_log\n" +
		extra
}
