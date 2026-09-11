// Package store defines persistence boundaries for SMART snapshots.
package store

import (
	"context"
	"log/slog"

	"smart-log-importer/internal/config"
	"smart-log-importer/internal/model"
)

// Store persists parsed SMART snapshots.
type Store interface {
	Initialize(ctx context.Context) error
	Upsert(ctx context.Context, log model.SmartLog) error
	Close()
}

// Open creates a Store for the configured database backend.
func Open(ctx context.Context, cfg config.DBConfig, logger *slog.Logger) (Store, error) {
	return openPostgres(ctx, cfg, logger)
}
