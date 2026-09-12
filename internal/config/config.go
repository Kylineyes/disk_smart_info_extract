// Package config loads and validates importer configuration.
package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// DBConfig describes the remote database selected by db.yaml.
type DBConfig struct {
	Type     string `yaml:"type"`
	Host     string `yaml:"host"`
	Port     uint16 `yaml:"port"`
	User     string `yaml:"user"`
	Password string `yaml:"password"`
	Database string `yaml:"database"`
	Table    string `yaml:"table"`
	SSLMode  string `yaml:"sslmode,omitempty"`
}

var simpleIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// LoadDBConfig reads a YAML database configuration and validates it.
//
// Errors intentionally do not include the underlying YAML text or its values,
// because this file contains credentials in normal use.
func LoadDBConfig(path string) (DBConfig, error) {
	var cfg DBConfig

	contents, err := os.ReadFile(path)
	if err != nil {
		return DBConfig{}, errors.New("read database configuration: unable to read file")
	}

	decoder := yaml.NewDecoder(strings.NewReader(string(contents)))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return DBConfig{}, errors.New("decode database configuration: invalid YAML")
	}

	// A configuration file must contain exactly one YAML document. This also
	// avoids silently ignoring a second document that could alter expectations.
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return DBConfig{}, errors.New("decode database configuration: expected one YAML document")
	}

	if err := cfg.Validate(); err != nil {
		return DBConfig{}, err
	}
	return cfg, nil
}

// Validate checks that a DBConfig is supported and safe to use.
func (cfg DBConfig) Validate() error {
	if cfg.Type != "postgres" {
		return errors.New("database type must be postgres")
	}
	if strings.TrimSpace(cfg.Host) == "" {
		return errors.New("database host is required")
	}
	if cfg.Port < 1 || cfg.Port > 65535 {
		return errors.New("database port must be between 1 and 65535")
	}
	if strings.TrimSpace(cfg.User) == "" {
		return errors.New("database user is required")
	}
	if strings.TrimSpace(cfg.Password) == "" {
		return errors.New("database password is required")
	}
	if strings.TrimSpace(cfg.Database) == "" {
		return errors.New("database name is required")
	}
	if cfg.Table == "" {
		return errors.New("database table is required")
	}
	if !simpleIdentifier.MatchString(cfg.Table) {
		return errors.New("database table must be a simple identifier")
	}
	if len(cfg.Table) > 63 {
		return errors.New("database table must not exceed 63 bytes")
	}
	return nil
}

// IsSimpleIdentifier reports whether name can safely be used as a quoted table
// identifier. It is exported for storage implementations that need to perform
// the same boundary check before interpolating an identifier into SQL.
func IsSimpleIdentifier(name string) bool {
	return simpleIdentifier.MatchString(name)
}

// String returns a non-sensitive summary of the configuration.
func (cfg DBConfig) String() string {
	return fmt.Sprintf(
		"type=%s host=%s port=%d database=%s table=%s sslmode=%s",
		cfg.Type,
		cfg.Host,
		cfg.Port,
		cfg.Database,
		cfg.Table,
		cfg.SSLMode,
	)
}
