// Package config loads and validates importer configuration.
package config

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
