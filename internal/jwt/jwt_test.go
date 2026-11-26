package jwt_test

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wolfeidau/vendo/internal/jwt"
)

func generateRSAKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	return key
}

func encodePKCS1(key *rsa.PrivateKey) []byte {
	return pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
}

func encodePKCS8(t *testing.T, key any) []byte {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: der,
	})
}

func TestParsePrivateKey_PKCS1AndPKCS8(t *testing.T) {
	orig := generateRSAKey(t)

	tests := []struct {
		name     string
		pemBytes []byte
	}{
		{
			name:     "PKCS#1 RSA PRIVATE KEY",
			pemBytes: encodePKCS1(orig),
		},
		{
			name:     "PKCS#8 PRIVATE KEY",
			pemBytes: encodePKCS8(t, orig),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			parsed, err := jwt.ParsePrivateKey(tc.pemBytes)
			require.NoError(t, err)
			require.True(t, orig.Equal(parsed))
		})
	}
}

func TestParsePrivateKey_Errors(t *testing.T) {
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	ecDER, err := x509.MarshalECPrivateKey(ecKey)
	require.NoError(t, err)
	ecPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "EC PRIVATE KEY",
		Bytes: ecDER,
	})

	rsaKey := generateRSAKey(t)
	pubDER, err := x509.MarshalPKIXPublicKey(&rsaKey.PublicKey)
	require.NoError(t, err)
	pubPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: pubDER,
	})

	corruptPKCS1 := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: []byte("not-valid-asn1"),
	})
	corruptPKCS8 := pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: []byte("not-valid-asn1"),
	})

	tests := []struct {
		name     string
		pemBytes []byte
		wantErr  string
	}{
		{
			name:     "empty input",
			pemBytes: nil,
			wantErr:  "no PEM block found",
		},
		{
			name:     "non-PEM data",
			pemBytes: []byte("plain text secret"),
			wantErr:  "no PEM block found",
		},
		{
			name:     "corrupt PKCS#1 DER",
			pemBytes: corruptPKCS1,
			wantErr:  "parse PKCS#1 private key",
		},
		{
			name:     "corrupt PKCS#8 DER",
			pemBytes: corruptPKCS8,
			wantErr:  "parse PKCS#8 private key",
		},
		{
			name:     "ECDSA key in PKCS#8",
			pemBytes: encodePKCS8(t, ecKey),
			wantErr:  "expected RSA private key",
		},
		{
			name:     "EC PRIVATE KEY block",
			pemBytes: ecPEM,
			wantErr:  "unsupported PEM block type",
		},
		{
			name:     "PUBLIC KEY block",
			pemBytes: pubPEM,
			wantErr:  "unsupported PEM block type",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			key, err := jwt.ParsePrivateKey(tc.pemBytes)
			require.Nil(t, key)
			require.Error(t, err)
			require.ErrorContains(t, err, tc.wantErr)
		})
	}
}

func TestSign_RS256AndClockSkew(t *testing.T) {
	orig := generateRSAKey(t)
	pkcs1Key, err := jwt.ParsePrivateKey(encodePKCS1(orig))
	require.NoError(t, err)
	pkcs8Key, err := jwt.ParsePrivateKey(encodePKCS8(t, orig))
	require.NoError(t, err)

	now := time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name  string
		appID string
		key   *rsa.PrivateKey
	}{
		{
			name:  "numeric app ID with PKCS#1 key",
			appID: "123456",
			key:   pkcs1Key,
		},
		{
			name:  "client ID with PKCS#8 key",
			appID: "Iv1.0123456789abcdef",
			key:   pkcs8Key,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			token, err := jwt.Sign(tc.appID, tc.key, now)
			require.NoError(t, err)

			parts := strings.Split(token, ".")
			require.Len(t, parts, 3)

			headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
			require.NoError(t, err)
			var hdr map[string]string
			require.NoError(t, json.Unmarshal(headerBytes, &hdr))
			require.Equal(t, map[string]string{
				"alg": "RS256",
				"typ": "JWT",
			}, hdr)

			claimsBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
			require.NoError(t, err)
			var payload struct {
				Iss string `json:"iss"`
				Iat int64  `json:"iat"`
				Exp int64  `json:"exp"`
			}
			require.NoError(t, json.Unmarshal(claimsBytes, &payload))
			require.Equal(t, tc.appID, payload.Iss)
			require.Equal(t, now.Add(-60*time.Second).Unix(), payload.Iat)
			require.Equal(t, now.Add(5*time.Minute).Unix(), payload.Exp)
			require.Equal(t, int64(360), payload.Exp-payload.Iat)

			sigBytes, err := base64.RawURLEncoding.DecodeString(parts[2])
			require.NoError(t, err)
			digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
			require.NoError(t, rsa.VerifyPKCS1v15(&orig.PublicKey, crypto.SHA256, digest[:], sigBytes))
		})
	}
}

func TestSign_ValidationErrors(t *testing.T) {
	key := generateRSAKey(t)
	now := time.Now()

	tests := []struct {
		name    string
		appID   string
		key     *rsa.PrivateKey
		wantErr string
	}{
		{
			name:    "empty appID",
			appID:   "",
			key:     key,
			wantErr: "app ID must not be empty",
		},
		{
			name:    "whitespace appID",
			appID:   "   \t",
			key:     key,
			wantErr: "app ID must not be empty",
		},
		{
			name:    "nil key",
			appID:   "123456",
			key:     nil,
			wantErr: "private key must not be nil",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			token, err := jwt.Sign(tc.appID, tc.key, now)
			require.Empty(t, token)
			require.Error(t, err)
			require.ErrorContains(t, err, tc.wantErr)
		})
	}
}
