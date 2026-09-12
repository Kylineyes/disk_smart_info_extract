// Command smart-log-importer imports one smartctl NVMe report into a database.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"smart-log-importer/internal/applog"
	"smart-log-importer/internal/config"
	"smart-log-importer/internal/model"
	"smart-log-importer/internal/parser"
	"smart-log-importer/internal/store"
)

const operationTimeout = 30 * time.Second

type importerDeps struct {
	loadDBConfig  func(string) (config.DBConfig, error)
	parseNVMeFile func(string) (model.SmartLog, error)
	openStore     func(context.Context, config.DBConfig, *slog.Logger) (store.Store, error)
}

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		os.Exit(1)
	}
}

// run executes one import using the production dependencies. It deliberately
// accepts its streams so callers can test argument handling and verify that
// data and diagnostics use separate output channels.
func run(args []string, stdout, stderr io.Writer) error {
	return runWithDeps(args, stdout, stderr, importerDeps{
		loadDBConfig:  config.LoadDBConfig,
		parseNVMeFile: parser.ParseNVMeFile,
		openStore:     store.Open,
	})
}

// runWithDeps executes one import with explicitly supplied dependencies. The
// dependency bundle is local to each invocation, so concurrent runs cannot
// race through mutable package-level test hooks.
func runWithDeps(args []string, stdout, stderr io.Writer, deps importerDeps) error {
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = os.Stderr
	}

	configPath, filePath, level, format, err := parseFlags(args, stderr)
	if err != nil {
		return err
	}

	logger, err := applog.New(stderr, level, format)
	if err != nil {
		writeUsageError(stderr, err)
		return err
	}

	started := time.Now()
	logger.Info("import started",
		"component", "import",
		"file", filePath,
	)
	defer func() {
		logger.Info("import finished",
			"component", "import",
			"file", filePath,
			"duration", time.Since(started).String(),
		)
	}()

	if deps.loadDBConfig == nil || deps.parseNVMeFile == nil || deps.openStore == nil {
		return errors.New("import dependencies are not configured")
	}

	cfg, err := deps.loadDBConfig(configPath)
	if err != nil {
		return importFailure(logger, filePath, started, fmt.Errorf("load database config: %w", err))
	}
	logger.Info("database config loaded",
		"component", "config",
		"table", cfg.Table,
	)

	smartLog, err := deps.parseNVMeFile(filePath)
	if err != nil {
		return importFailure(logger, filePath, started, fmt.Errorf("parse smart log: %w", err))
	}
	logger.Info("smart log parsed",
		"component", "parser",
		"file", filePath,
		"snapshot_date", smartLog.SnapshotDate.Format("2006-01-02"),
		"model", smartLog.Device.Model,
		"serial_suffix", applog.SerialSuffix(smartLog.Device.Serial),
	)

	connectCtx, cancel := context.WithTimeout(context.Background(), operationTimeout)
	st, err := deps.openStore(connectCtx, cfg, logger)
	cancel()
	if err != nil {
		if st != nil {
			st.Close()
		}
		return importFailure(
			logger,
			filePath,
			started,
			fmt.Errorf("open database store: %w", err),
			cfg.Password,
			smartLog.Device.Serial,
		)
	}
	if st == nil {
		return importFailure(
			logger,
			filePath,
			started,
			errors.New("open database store: returned a nil store"),
			cfg.Password,
			smartLog.Device.Serial,
		)
	}
	defer st.Close()
	logger.Info("database connected",
		"component", "store",
		"table", cfg.Table,
	)

	initializeCtx, cancel := context.WithTimeout(context.Background(), operationTimeout)
	err = st.Initialize(initializeCtx)
	cancel()
	if err != nil {
		return importFailure(
			logger,
			filePath,
			started,
			fmt.Errorf("initialize database: %w", err),
			cfg.Password,
			smartLog.Device.Serial,
		)
	}
	logger.Info("database initialized",
		"component", "store",
		"table", cfg.Table,
	)

	upsertCtx, cancel := context.WithTimeout(context.Background(), operationTimeout)
	err = st.Upsert(upsertCtx, smartLog)
	cancel()
	if err != nil {
		return importFailure(
			logger,
			filePath,
			started,
			fmt.Errorf("upsert smart log: %w", err),
			cfg.Password,
			smartLog.Device.Serial,
		)
	}
	logger.Info("smart log upserted",
		"component", "store",
		"table", cfg.Table,
		"snapshot_date", smartLog.SnapshotDate.Format("2006-01-02"),
		"model", smartLog.Device.Model,
		"serial_suffix", applog.SerialSuffix(smartLog.Device.Serial),
	)

	_, err = fmt.Fprintf(stdout,
		"imported smart log: date=%s model=%q serial=%q\n",
		smartLog.SnapshotDate.Format("2006-01-02"),
		smartLog.Device.Model, "..."+applog.SerialSuffix(smartLog.Device.Serial),
	)
	if err != nil {
		return importFailure(logger, filePath, started, fmt.Errorf("write success summary: %w", err))
	}
	return nil
}

func parseFlags(args []string, stderr io.Writer) (configPath, filePath, level, format string, err error) {
	fs := flag.NewFlagSet("smart-log-importer", flag.ContinueOnError)
	// We print a compact, stable usage message ourselves. The flag package's
	// default diagnostics can otherwise be mixed into command output.
	fs.SetOutput(io.Discard)
	fs.StringVar(&configPath, "c", "", "path to database YAML")
	fs.StringVar(&filePath, "f", "", "path to smartctl log")
	fs.StringVar(&level, "log-level", "info", "log level")
	fs.StringVar(&format, "log-format", "text", "log format")
	if parseErr := fs.Parse(args); parseErr != nil {
		writeUsageError(stderr, parseErr)
		return "", "", "", "", parseErr
	}
	if extras := fs.Args(); len(extras) != 0 {
		parseErr := fmt.Errorf("unexpected argument %q", extras[0])
		writeUsageError(stderr, parseErr)
		return "", "", "", "", parseErr
	}
	if strings.TrimSpace(configPath) == "" {
		err = errors.New("missing required -c database config path")
	} else if strings.TrimSpace(filePath) == "" {
		err = errors.New("missing required -f smartctl log path")
	}
	if err != nil {
		writeUsageError(stderr, err)
	}
	return configPath, filePath, level, format, err
}

func writeUsageError(stderr io.Writer, err error) {
	if stderr == nil {
		stderr = os.Stderr
	}
	fmt.Fprintln(stderr,
		"usage: smart-log-importer -c <db.yaml> -f <smartctl-log> "+
			"[-log-level debug|info|warn|error] [-log-format text|json]",
	)
	if err != nil {
		fmt.Fprintf(stderr, "error: %s\n", err)
	}
}

func importFailure(logger *slog.Logger, filePath string, started time.Time, err error, sensitive ...string) error {
	logError := err.Error()
	for _, value := range sensitive {
		if value != "" {
			logError = strings.ReplaceAll(logError, value, "[redacted]")
		}
	}
	logger.Error("import failed",
		"component", "import",
		"file", filePath,
		"error", logError,
		"duration", time.Since(started).String(),
	)
	return err
}
