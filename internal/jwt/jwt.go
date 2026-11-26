package jwt

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	clockSkewBuffer = 60 * time.Second
	tokenLifetime   = 5 * time.Minute
)

type header struct {
	Alg string `json:"alg"`
	Typ string `json:"typ"`
}

type claims struct {
	Iss string `json:"iss"`
	Iat int64  `json:"iat"`
	Exp int64  `json:"exp"`
}

func ParsePrivateKey(pemBytes []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("invalid PEM data: no PEM block found")
	}

	var key *rsa.PrivateKey
	switch block.Type {
	case "RSA PRIVATE KEY":
		parsed, err := x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse PKCS#1 private key: %w", err)
		}
		key = parsed
	case "PRIVATE KEY":
		parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse PKCS#8 private key: %w", err)
		}
		rsaKey, ok := parsed.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("unsupported private key type %T: expected RSA private key", parsed)
		}
		key = rsaKey
	default:
		return nil, fmt.Errorf("unsupported PEM block type %q: expected RSA PRIVATE KEY or PRIVATE KEY", block.Type)
	}

	if err := key.Validate(); err != nil {
		return nil, fmt.Errorf("validate RSA private key: %w", err)
	}
	return key, nil
}

func Sign(appID string, key *rsa.PrivateKey, now time.Time) (string, error) {
	if strings.TrimSpace(appID) == "" {
		return "", errors.New("app ID must not be empty")
	}
	if key == nil {
		return "", errors.New("private key must not be nil")
	}

	headerJSON, err := json.Marshal(header{
		Alg: "RS256",
		Typ: "JWT",
	})
	if err != nil {
		return "", fmt.Errorf("marshal JWT header: %w", err)
	}

	claimsJSON, err := json.Marshal(claims{
		Iss: appID,
		Iat: now.Add(-clockSkewBuffer).Unix(),
		Exp: now.Add(tokenLifetime).Unix(),
	})
	if err != nil {
		return "", fmt.Errorf("marshal JWT claims: %w", err)
	}

	signingInput := base64.RawURLEncoding.EncodeToString(headerJSON) + "." +
		base64.RawURLEncoding.EncodeToString(claimsJSON)

	digest := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("sign JWT: %w", err)
	}

	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}
