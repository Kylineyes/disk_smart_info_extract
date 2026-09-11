// Package applog initializes the application's structured logger.
package applog

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"unicode/utf8"
)

// New returns a structured application logger.
//
// The logger writes to out and never changes slog's process-wide default
// logger. An empty level or format uses the command's defaults (info and
// text, respectively). If out is nil, logs are written to os.Stderr.
func New(out io.Writer, level string, format string) (*slog.Logger, error) {
	if out == nil {
		out = os.Stderr
	}

	parsedLevel, err := parseLevel(level)
	if err != nil {
		return nil, err
	}

	if format == "" {
		format = "text"
	}
	var handler slog.Handler
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "text":
		handler = slog.NewTextHandler(out, &slog.HandlerOptions{Level: parsedLevel})
	case "json":
		handler = slog.NewJSONHandler(out, &slog.HandlerOptions{Level: parsedLevel})
	default:
		return nil, fmt.Errorf("invalid log format %q: expected text or json", format)
	}
	return slog.New(handler), nil
}

func parseLevel(level string) (slog.Level, error) {
	if level == "" {
		return slog.LevelInfo, nil
	}
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("invalid log level %q: expected debug, info, warn, or error", level)
	}
}

// SerialSuffix returns the final four characters of serial. Shorter serials
// are returned unchanged. It operates on runes so a non-ASCII serial is not
// split in the middle of a UTF-8 encoded character.
func SerialSuffix(serial string) string {
	if utf8.RuneCountInString(serial) <= 4 {
		return serial
	}
	runes := []rune(serial)
	return string(runes[len(runes)-4:])
}
