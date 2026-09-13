// Package smartctl executes smartctl commands for the import boundary.
package smartctl

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// Run executes smartctl --all for device and returns only its standard output.
//
// The command is started directly, without a shell. Standard error is
// discarded so diagnostics cannot become part of the SMART report or an error
// returned by this package. The returned error wraps context cancellation,
// command lookup, process start, or non-zero exit errors as appropriate.
func Run(ctx context.Context, device string) (string, error) {
	if strings.TrimSpace(device) == "" {
		return "", errors.New("smartctl device must not be empty")
	}
	if ctx == nil {
		return "", errors.New("smartctl context must not be nil")
	}
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("run smartctl: %w", err)
	}

	command := exec.CommandContext(ctx, "smartctl", "--all", device)
	var stdout bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = io.Discard

	if err := command.Run(); err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return "", fmt.Errorf("run smartctl: %w", contextErr)
		}

		var lookupErr *exec.Error
		if errors.As(err, &lookupErr) {
			return "", fmt.Errorf("look up smartctl: %w", err)
		}

		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return "", fmt.Errorf("smartctl --all exited unsuccessfully: %w", err)
		}

		return "", fmt.Errorf("start smartctl --all: %w", err)
	}
	return stdout.String(), nil
}
