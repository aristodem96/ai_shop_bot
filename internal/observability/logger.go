// Package observability builds the process logger.
package observability

import (
	"fmt"
	"log/slog"
	"os"
)

// NewLogger builds the process logger. format is "json" or "text".
func NewLogger(level, format string) (*slog.Logger, error) {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(level)); err != nil {
		return nil, fmt.Errorf("observability: unknown log level %q", level)
	}
	opts := &slog.HandlerOptions{Level: lvl}

	switch format {
	case "json":
		return slog.New(slog.NewJSONHandler(os.Stdout, opts)), nil
	case "text":
		return slog.New(slog.NewTextHandler(os.Stdout, opts)), nil
	default:
		return nil, fmt.Errorf("observability: unknown log format %q", format)
	}
}
