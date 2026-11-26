package keysource

import (
	"bytes"
	"context"
	"crypto/rsa"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/wolfeidau/vendo/internal/jwt"
)

const opWaitDelay = 2 * time.Second

func ValidateOpRef(ref string) error {
	if strings.ContainsAny(ref, "\r\n\x00") {
		return errors.New("invalid 1Password reference: must not contain control characters")
	}
	if !strings.HasPrefix(ref, "op://") {
		return errors.New("invalid 1Password reference: must start with op://")
	}

	rest := strings.TrimPrefix(ref, "op://")
	pathPart, queryPart, hasQuery := strings.Cut(rest, "?")
	if hasQuery && strings.TrimSpace(queryPart) == "" {
		return errors.New("invalid 1Password reference: query parameters must not be empty")
	}

	segments := strings.Split(pathPart, "/")
	if len(segments) != 3 && len(segments) != 4 {
		return errors.New("invalid 1Password reference: expected op://<vault>/<item>/<field> or op://<vault>/<item>/<section>/<field>")
	}
	for _, seg := range segments {
		if strings.TrimSpace(seg) == "" {
			return errors.New("invalid 1Password reference: path segments must not be empty")
		}
	}

	return nil
}

func LoadFromOp(ctx context.Context, ref string) (*rsa.PrivateKey, error) {
	if err := ValidateOpRef(ref); err != nil {
		return nil, err
	}

	cmd := exec.CommandContext(ctx, "op", "read", "--no-newline", ref)
	// Bound pipe wait after cancellation in case op leaves a child holding fds open.
	cmd.WaitDelay = opWaitDelay

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	pemBytes := stdout.Bytes()
	defer clear(pemBytes)
	defer clear(stderr.Bytes())

	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("1Password CLI canceled: %w", ctxErr)
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil, fmt.Errorf("1Password CLI failed with exit status %d (stderr redacted)", exitErr.ExitCode())
		}
		if errors.Is(err, exec.ErrNotFound) {
			return nil, fmt.Errorf("1Password CLI (op) not found in PATH: %w", err)
		}
		return nil, fmt.Errorf("run 1Password CLI: %w", err)
	}

	key, err := jwt.ParsePrivateKey(pemBytes)
	if err != nil {
		return nil, fmt.Errorf("parse private key from 1Password: %w", err)
	}
	return key, nil
}

func LoadFromFile(path string, stdin io.Reader) (*rsa.PrivateKey, error) {
	if path == "" {
		return nil, errors.New("key file path must not be empty")
	}

	var pemBytes []byte
	var err error
	if path == "-" {
		if stdin == nil {
			return nil, errors.New("stdin reader must not be nil")
		}
		pemBytes, err = io.ReadAll(stdin)
		if err != nil {
			return nil, fmt.Errorf("read private key from stdin: %w", err)
		}
	} else {
		pemBytes, err = os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read private key file: %w", err)
		}
	}
	defer clear(pemBytes)

	key, err := jwt.ParsePrivateKey(pemBytes)
	if err != nil {
		return nil, fmt.Errorf("parse private key: %w", err)
	}
	return key, nil
}
