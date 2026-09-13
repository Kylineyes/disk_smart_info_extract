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
	"smart-log-importer/internal/smartctl"
	"smart-log-importer/internal/store"
)

const operationTimeout = 30 * time.Second

type importerDeps struct {
	loadDBConfig  func(string) (config.DBConfig, error)
	parseNVMeFile func(string) (model.SmartLog, error)
	parseNVMe     func(string, string) (model.SmartLog, error)
	runSmartctl   func(context.Context, string) (string, error)
	openStore     func(context.Context, config.DBConfig, *slog.Logger) (store.Store, error)
}

type importSource struct {
	filePath   string
	devicePath string
}

func (source importSource) logAttrs() []any {
	if source.devicePath != "" {
		return []any{"device", source.devicePath}
	}
	return []any{"file", source.filePath}
}

func eventAttrs(source importSource, attrs ...any) []any {
	return append(append([]any(nil), attrs...), source.logAttrs()...)
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
		parseNVMe:     parser.ParseNVMe,
		runSmartctl:   smartctl.Run,
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

	configPath, filePath, devicePath, level, format, err := parseFlags(args, stderr)
	if err != nil {
		return err
	}
	source := importSource{filePath: filePath, devicePath: devicePath}

	logger, err := applog.New(stderr, level, format)
	if err != nil {
		writeUsageError(stderr, err)
		return err
	}

	started := time.Now()
	logger.Info("import started", eventAttrs(source, "component", "import")...)
	defer func() {
		logger.Info("import finished", eventAttrs(
			source,
			"component", "import",
			"duration", time.Since(started).String(),
		)...)
	}()

	if deps.loadDBConfig == nil || deps.openStore == nil ||
		(source.filePath != "" && deps.parseNVMeFile == nil) ||
		(source.devicePath != "" && (deps.parseNVMe == nil || deps.runSmartctl == nil)) {
		return errors.New("import dependencies are not configured")
	}

	cfg, err := deps.loadDBConfig(configPath)
	if err != nil {
		return importFailure(logger, source, started, fmt.Errorf("load database config: %w", err))
	}
	logger.Info("database config loaded", eventAttrs(source, "component", "config", "table", cfg.Table)...)

	var smartLog model.SmartLog
	if source.devicePath != "" {
		collectCtx, cancel := context.WithTimeout(context.Background(), operationTimeout)
		rawLog, collectErr := deps.runSmartctl(collectCtx, source.devicePath)
		cancel()
		if collectErr != nil {
			return importFailure(
				logger,
				source,
				started,
				fmt.Errorf("collect smart log: %w", collectErr),
				cfg.Password,
			)
		}
		smartLog, err = deps.parseNVMe(rawLog, source.devicePath)
	} else {
		smartLog, err = deps.parseNVMeFile(source.filePath)
	}
	if err != nil {
		return importFailure(logger, source, started, fmt.Errorf("parse smart log: %w", err))
	}
	logger.Info("smart log parsed", eventAttrs(
		source,
		"component", "parser",
		"snapshot_date", smartLog.SnapshotDate.Format("2006-01-02"),
		"model", smartLog.Device.Model,
		"serial_suffix", applog.SerialSuffix(smartLog.Device.Serial),
	)...)

	connectCtx, cancel := context.WithTimeout(context.Background(), operationTimeout)
	st, err := deps.openStore(connectCtx, cfg, logger)
	cancel()
	if err != nil {
		if st != nil {
			st.Close()
		}
		return importFailure(
			logger,
			source,
			started,
			fmt.Errorf("open database store: %w", err),
			cfg.Password,
			smartLog.Device.Serial,
		)
	}
	if st == nil {
		return importFailure(
			logger,
			source,
			started,
			errors.New("open database store: returned a nil store"),
			cfg.Password,
			smartLog.Device.Serial,
		)
	}
	defer st.Close()
	logger.Info("database connected", eventAttrs(source, "component", "store", "table", cfg.Table)...)

	initializeCtx, cancel := context.WithTimeout(context.Background(), operationTimeout)
	err = st.Initialize(initializeCtx)
	cancel()
	if err != nil {
		return importFailure(
			logger,
			source,
			started,
			fmt.Errorf("initialize database: %w", err),
			cfg.Password,
			smartLog.Device.Serial,
		)
	}
	logger.Info("database initialized", eventAttrs(source, "component", "store", "table", cfg.Table)...)

	upsertCtx, cancel := context.WithTimeout(context.Background(), operationTimeout)
	err = st.Upsert(upsertCtx, smartLog)
	cancel()
	if err != nil {
		return importFailure(
			logger,
			source,
			started,
			fmt.Errorf("upsert smart log: %w", err),
			cfg.Password,
			smartLog.Device.Serial,
		)
	}
	logger.Info("smart log upserted", eventAttrs(
		source,
		"component", "store",
		"table", cfg.Table,
		"snapshot_date", smartLog.SnapshotDate.Format("2006-01-02"),
		"model", smartLog.Device.Model,
		"serial_suffix", applog.SerialSuffix(smartLog.Device.Serial),
	)...)

	_, err = fmt.Fprintf(stdout,
		"imported smart log: date=%s model=%q serial=%q\n",
		smartLog.SnapshotDate.Format("2006-01-02"),
		smartLog.Device.Model, "..."+applog.SerialSuffix(smartLog.Device.Serial),
	)
	if err != nil {
		return importFailure(logger, source, started, fmt.Errorf("write success summary: %w", err))
	}
	return nil
}

const usageText = "usage: smart-log-importer -c <db.yaml> (-f <smartctl-log> | -d <device>) " +
	"[-log-level debug|info|warn|error] [-log-format text|json]"

func parseFlags(args []string, stderr io.Writer) (configPath, filePath, devicePath, level, format string, err error) {
	fs := flag.NewFlagSet("smart-log-importer", flag.ContinueOnError)
	// We print a compact, stable usage message ourselves. The flag package's
	// default diagnostics can otherwise be mixed into command output.
	fs.SetOutput(io.Discard)
	fs.StringVar(&configPath, "c", "", "path to database YAML")
	fs.StringVar(&filePath, "f", "", "path to smartctl log")
	fs.StringVar(&devicePath, "d", "", "device path")
	fs.StringVar(&level, "log-level", "info", "log level")
	fs.StringVar(&format, "log-format", "text", "log format")
	if parseErr := fs.Parse(args); parseErr != nil {
		writeUsageError(stderr, parseErr)
		return "", "", "", "", "", parseErr
	}
	if extras := fs.Args(); len(extras) != 0 {
		parseErr := fmt.Errorf("unexpected argument %q", extras[0])
		writeUsageError(stderr, parseErr)
		return "", "", "", "", "", parseErr
	}

	var problems []string
	if strings.TrimSpace(configPath) == "" {
		problems = append(problems, "missing required -c database config path")
	}
	hasFile := strings.TrimSpace(filePath) != ""
	hasDevice := strings.TrimSpace(devicePath) != ""
	switch {
	case hasFile && hasDevice:
		problems = append(problems, "-f and -d cannot be used together; provide exactly one input")
	case !hasFile && !hasDevice:
		problems = append(problems, "exactly one of -f or -d is required")
	}
	if len(problems) != 0 {
		err = errors.New(strings.Join(problems, "; "))
		writeUsageError(stderr, err)
	}
	return configPath, filePath, devicePath, level, format, err
}

func writeUsageError(stderr io.Writer, err error) {
	if stderr == nil {
		stderr = os.Stderr
	}
	fmt.Fprintln(stderr, usageText)
	if err != nil {
		fmt.Fprintf(stderr, "error: %s\n", err)
	}
}

func importFailure(logger *slog.Logger, source importSource, started time.Time, err error, sensitive ...string) error {
	logError := err.Error()
	for _, value := range sensitive {
		if value != "" {
			logError = strings.ReplaceAll(logError, value, "[redacted]")
		}
	}
	logger.Error("import failed", eventAttrs(
		source,
		"component", "import",
		"error", logError,
		"duration", time.Since(started).String(),
	)...)
	return err
}
