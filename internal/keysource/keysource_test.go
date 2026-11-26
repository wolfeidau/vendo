package keysource_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wolfeidau/vendo/internal/keysource"
)

func generateKeyPEM(t *testing.T) (*rsa.PrivateKey, []byte) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	pemBytes := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	return key, pemBytes
}

func installFakeOp(t *testing.T, script string) string {
	t.Helper()
	binDir := t.TempDir()
	opPath := filepath.Join(binDir, "op")
	require.NoError(t, os.WriteFile(opPath, []byte(script), 0o700))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return binDir
}

func TestValidateOpRef(t *testing.T) {
	valid := []struct {
		name string
		ref  string
	}{
		{
			name: "three segments",
			ref:  "op://test-vault/test-item/private-key",
		},
		{
			name: "four segments with section",
			ref:  "op://test-vault/test-item/test-section/private-key",
		},
		{
			name: "three segments with query",
			ref:  "op://test-vault/test-item/private-key?attribute=otp",
		},
		{
			name: "four segments with query and spaces",
			ref:  "op://My Vault/My App/Credentials/Private Key?ssh-format=openssh",
		},
	}

	for _, tc := range valid {
		t.Run("valid/"+tc.name, func(t *testing.T) {
			require.NoError(t, keysource.ValidateOpRef(tc.ref))
		})
	}

	invalid := []struct {
		name   string
		ref    string
		secret string
	}{
		{
			name:   "empty",
			ref:    "",
			secret: "",
		},
		{
			name:   "missing op prefix",
			ref:    "https://secret-vault/secret-item/secret-field",
			secret: "secret-vault",
		},
		{
			name:   "leading space",
			ref:    " op://secret-vault/secret-item/secret-field",
			secret: "secret-vault",
		},
		{
			name:   "two segments only",
			ref:    "op://secret-vault/secret-item",
			secret: "secret-vault",
		},
		{
			name:   "five segments",
			ref:    "op://secret-vault/item/sec/sub/field",
			secret: "secret-vault",
		},
		{
			name:   "empty segment",
			ref:    "op://secret-vault//field",
			secret: "secret-vault",
		},
		{
			name:   "whitespace-only segment",
			ref:    "op://secret-vault/   /field",
			secret: "secret-vault",
		},
		{
			name:   "trailing slash",
			ref:    "op://secret-vault/item/field/",
			secret: "secret-vault",
		},
		{
			name:   "empty query",
			ref:    "op://secret-vault/item/field?",
			secret: "secret-vault",
		},
		{
			name:   "newline control char",
			ref:    "op://secret-vault/item/field\nextra",
			secret: "secret-vault",
		},
	}

	for _, tc := range invalid {
		t.Run("invalid/"+tc.name, func(t *testing.T) {
			err := keysource.ValidateOpRef(tc.ref)
			require.Error(t, err)
			if tc.secret != "" {
				require.NotContains(t, err.Error(), tc.secret)
			}
			if tc.ref != "" {
				require.NotContains(t, err.Error(), tc.ref)
			}
		})
	}
}

func TestLoadFromFile(t *testing.T) {
	origKey, pemBytes := generateKeyPEM(t)

	t.Run("from file path", func(t *testing.T) {
		keyPath := filepath.Join(t.TempDir(), "key.pem")
		require.NoError(t, os.WriteFile(keyPath, pemBytes, 0o600))

		loaded, err := keysource.LoadFromFile(keyPath, nil)
		require.NoError(t, err)
		require.True(t, origKey.Equal(loaded))
	})

	t.Run("from stdin", func(t *testing.T) {
		loaded, err := keysource.LoadFromFile("-", bytes.NewReader(pemBytes))
		require.NoError(t, err)
		require.True(t, origKey.Equal(loaded))
	})

	t.Run("empty path", func(t *testing.T) {
		loaded, err := keysource.LoadFromFile("", nil)
		require.Nil(t, loaded)
		require.Error(t, err)
	})

	t.Run("stdin with nil reader", func(t *testing.T) {
		loaded, err := keysource.LoadFromFile("-", nil)
		require.Nil(t, loaded)
		require.Error(t, err)
	})

	t.Run("missing file", func(t *testing.T) {
		loaded, err := keysource.LoadFromFile(filepath.Join(t.TempDir(), "missing.pem"), nil)
		require.Nil(t, loaded)
		require.Error(t, err)
		require.True(t, errors.Is(err, os.ErrNotExist))
	})

	t.Run("invalid PEM content", func(t *testing.T) {
		loaded, err := keysource.LoadFromFile("-", bytes.NewBufferString("not-a-pem"))
		require.Nil(t, loaded)
		require.Error(t, err)
		require.ErrorContains(t, err, "parse private key")
	})
}

func TestLoadFromOp_Success(t *testing.T) {
	origKey, pemBytes := generateKeyPEM(t)
	pemPath := filepath.Join(t.TempDir(), "key.pem")
	require.NoError(t, os.WriteFile(pemPath, pemBytes, 0o600))

	expectedRef := "op://Test Vault/Test App/Section/Private Key?attribute=pem"
	argsFile := filepath.Join(t.TempDir(), "args.txt")

	installFakeOp(t, `#!/bin/sh
printf '%s\n' "$@" > "`+argsFile+`"
cat "`+pemPath+`"
`)

	loaded, err := keysource.LoadFromOp(context.Background(), expectedRef)
	require.NoError(t, err)
	require.True(t, origKey.Equal(loaded))

	argsBytes, err := os.ReadFile(argsFile)
	require.NoError(t, err)
	require.Equal(t, "read\n--no-newline\n"+expectedRef+"\n", string(argsBytes))
}

func TestLoadFromOp_ValidatesUpfrontWithoutInvokingOp(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	badRef := "op://sensitive-vault/only-two-segments"
	loaded, err := keysource.LoadFromOp(context.Background(), badRef)
	require.Nil(t, loaded)
	require.Error(t, err)
	require.ErrorContains(t, err, "invalid 1Password reference")
	require.NotContains(t, err.Error(), "sensitive-vault")
	require.NotContains(t, err.Error(), "only-two-segments")
}

func TestLoadFromOp_RedactsStderrAndOpRef(t *testing.T) {
	ref := "op://private-vault-name/private-item-name/private-field-name"
	stderrSentinel := "SENSITIVE_STDERR_DETAIL_12345"

	installFakeOp(t, `#!/bin/sh
echo "[ERROR] `+stderrSentinel+` for $3" >&2
exit 42
`)

	loaded, err := keysource.LoadFromOp(context.Background(), ref)
	require.Nil(t, loaded)
	require.Error(t, err)
	require.ErrorContains(t, err, "42")
	require.NotContains(t, err.Error(), stderrSentinel)
	require.NotContains(t, err.Error(), "private-vault-name")
	require.NotContains(t, err.Error(), "private-item-name")
	require.NotContains(t, err.Error(), "private-field-name")
	require.NotContains(t, err.Error(), "op://")
}

func TestLoadFromOp_TimeoutCancellation(t *testing.T) {
	ref := "op://timeout-vault/timeout-item/timeout-field"

	installFakeOp(t, `#!/bin/sh
exec sleep 10
`)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	loaded, err := keysource.LoadFromOp(ctx, ref)
	require.Nil(t, loaded)
	require.Error(t, err)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.NotContains(t, err.Error(), "timeout-vault")
	require.NotContains(t, err.Error(), "op://")
}

func TestLoadFromOp_InvalidPEMOutput(t *testing.T) {
	ref := "op://test-vault/test-item/bad-pem"

	installFakeOp(t, `#!/bin/sh
printf 'not a valid pem key'
`)

	loaded, err := keysource.LoadFromOp(context.Background(), ref)
	require.Nil(t, loaded)
	require.Error(t, err)
	require.ErrorContains(t, err, "parse private key from 1Password")
	require.NotContains(t, err.Error(), "test-vault")
	require.NotContains(t, err.Error(), "op://")
}
