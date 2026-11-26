package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/wolfeidau/vendo/internal/commands"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	var cli commands.CLI
	globals := &commands.Globals{
		Version: version,
		Commit:  commit,
		Date:    date,
		Stdin:   os.Stdin,
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
	}

	parser, err := commands.NewParser(&cli, globals)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to initialize CLI: %v\n", err)
		return 1
	}

	kctx, err := parser.Parse(args)
	if err != nil {
		parser.FatalIfErrorf(err)
		return 1
	}

	logger, err := newLogger(cli.LogLevel, cli.LogFormat, os.Stderr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to configure logger: %v\n", err)
		return 1
	}
	globals.Logger = logger

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	kctx.BindTo(ctx, (*context.Context)(nil))
	kctx.Bind(globals)

	if err := kctx.Run(); err != nil {
		var exitErr *commands.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.Code
		}
		logger.ErrorContext(ctx, err.Error())
		return 1
	}

	return 0
}

func newLogger(levelStr, format string, w io.Writer) (*slog.Logger, error) {
	var level slog.Level
	switch levelStr {
	case "debug":
		level = slog.LevelDebug
	case "info":
		level = slog.LevelInfo
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		return nil, fmt.Errorf("unsupported log level %q", levelStr)
	}

	opts := &slog.HandlerOptions{Level: level}
	switch format {
	case "text":
		return slog.New(slog.NewTextHandler(w, opts)), nil
	case "json":
		return slog.New(slog.NewJSONHandler(w, opts)), nil
	default:
		return nil, fmt.Errorf("unsupported log format %q", format)
	}
}
