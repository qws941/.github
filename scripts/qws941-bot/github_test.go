package main

import (
	"bytes"
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func mustGenerateRSAKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate rsa key: %v", err)
	}
	return key
}

func TestVerifyWebhookSignature(t *testing.T) {
	secret := "top-secret"
	body := []byte(`{"hello":"world"}`)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	req := httptest.NewRequest("POST", "/webhook", bytes.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", sig)

	got, err := verifyWebhookSignature(req, secret)
	if err != nil {
		t.Fatalf("verifyWebhookSignature error: %v", err)
	}
	if string(got) != string(body) {
		t.Fatalf("body = %q, want %q", string(got), string(body))
	}
}

func TestVerifyWebhookSignatureErrors(t *testing.T) {
	secret := "top-secret"
	body := []byte(`{"k":"v"}`)

	cases := []struct {
		name string
		sig  string
	}{
		{name: "missing", sig: ""},
		{name: "bad-prefix", sig: "md5=abcd"},
		{name: "bad-hex", sig: "sha256=zzzz"},
		{name: "mismatch", sig: "sha256=deadbeef"},
	}

	for _, c := range cases {
		req := httptest.NewRequest("POST", "/webhook", bytes.NewReader(body))
		if c.sig != "" {
			req.Header.Set("X-Hub-Signature-256", c.sig)
		}
		if _, err := verifyWebhookSignature(req, secret); err == nil {
			t.Fatalf("%s: expected error, got nil", c.name)
		}
	}
}

func TestGenerateAppJWT(t *testing.T) {
	key := mustGenerateRSAKey(t)
	token, err := generateAppJWT("12345", key)
	if err != nil {
		t.Fatalf("generateAppJWT error: %v", err)
	}

	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("jwt parts = %d, want 3", len(parts))
	}

	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatalf("decode header: %v", err)
	}
	var header map[string]any
	if err := json.Unmarshal(headerJSON, &header); err != nil {
		t.Fatalf("unmarshal header: %v", err)
	}
	if header["alg"] != "RS256" || header["typ"] != "JWT" {
		t.Fatalf("unexpected header: %v", header)
	}

	payloadJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(payloadJSON, &claims); err != nil {
		t.Fatalf("unmarshal claims: %v", err)
	}
	if claims["iss"] != "12345" {
		t.Fatalf("iss = %v, want 12345", claims["iss"])
	}
	iat, ok := claims["iat"].(float64)
	if !ok {
		t.Fatalf("iat type = %T, want float64", claims["iat"])
	}
	exp, ok := claims["exp"].(float64)
	if !ok {
		t.Fatalf("exp type = %T, want float64", claims["exp"])
	}
	if exp <= iat {
		t.Fatalf("exp (%v) must be greater than iat (%v)", exp, iat)
	}

	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("decode signature: %v", err)
	}
	hash := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, hash[:], sig); err != nil {
		t.Fatalf("signature verification failed: %v", err)
	}
}

func TestBase64URLEncode(t *testing.T) {
	input := []byte("hello world")
	encoded := base64URLEncode(input)
	if strings.Contains(encoded, "=") {
		t.Fatalf("encoded string has padding: %q", encoded)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode encoded string: %v", err)
	}
	if string(decoded) != string(input) {
		t.Fatalf("decoded = %q, want %q", string(decoded), string(input))
	}
}

func TestParseRSAPrivateKey_PKCS1AndPKCS8(t *testing.T) {
	key := mustGenerateRSAKey(t)

	pkcs1PEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	pkcs1Key, err := parseRSAPrivateKey(pkcs1PEM)
	if err != nil {
		t.Fatalf("parse PKCS1 key: %v", err)
	}
	if pkcs1Key.N.Cmp(key.N) != 0 || pkcs1Key.D.Cmp(key.D) != 0 {
		t.Fatalf("parsed PKCS1 key does not match original")
	}

	pkcs8Bytes, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal PKCS8: %v", err)
	}
	pkcs8PEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8Bytes})
	pkcs8Key, err := parseRSAPrivateKey(pkcs8PEM)
	if err != nil {
		t.Fatalf("parse PKCS8 key: %v", err)
	}
	if pkcs8Key.N.Cmp(key.N) != 0 || pkcs8Key.D.Cmp(key.D) != 0 {
		t.Fatalf("parsed PKCS8 key does not match original")
	}
}

func TestParseRSAPrivateKeyErrors(t *testing.T) {
	if _, err := parseRSAPrivateKey([]byte("not pem")); err == nil {
		t.Fatalf("expected no PEM block error")
	}

	unsupported := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("abc")})
	if _, err := parseRSAPrivateKey(unsupported); err == nil {
		t.Fatalf("expected unsupported PEM block type error")
	}
}

func TestLoadPrivateKeyFile_PathAndInline(t *testing.T) {
	key := mustGenerateRSAKey(t)
	pemData := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})

	tempDir := t.TempDir()
	path := tempDir + "/app.pem"
	if err := os.WriteFile(path, pemData, 0o600); err != nil {
		t.Fatalf("write temp key file: %v", err)
	}

	fromPath, err := loadPrivateKeyFile(path)
	if err != nil {
		t.Fatalf("loadPrivateKeyFile(path) error: %v", err)
	}
	if fromPath.N.Cmp(key.N) != 0 {
		t.Fatalf("path mode key mismatch")
	}

	fromInline, err := loadPrivateKeyFile(string(pemData))
	if err != nil {
		t.Fatalf("loadPrivateKeyFile(inline) error: %v", err)
	}
	if fromInline.N.Cmp(key.N) != 0 {
		t.Fatalf("inline mode key mismatch")
	}
}

func TestParseBigInt(t *testing.T) {
	got, err := parseBigInt("12345")
	if err != nil {
		t.Fatalf("parseBigInt valid error: %v", err)
	}
	if got != 12345 {
		t.Fatalf("parseBigInt valid = %d, want 12345", got)
	}

	if _, err := parseBigInt("abc"); err == nil {
		t.Fatalf("parseBigInt invalid expected error")
	}
}

func TestTokenCacheValid(t *testing.T) {
	cases := []struct {
		name  string
		cache tokenCache
		want  bool
	}{
		{
			name:  "empty-token",
			cache: tokenCache{token: "", expiresAt: time.Now().Add(10 * time.Minute)},
			want:  false,
		},
		{
			name:  "expired",
			cache: tokenCache{token: "x", expiresAt: time.Now().Add(-1 * time.Minute)},
			want:  false,
		},
		{
			name:  "near-expiry-buffer-invalid",
			cache: tokenCache{token: "x", expiresAt: time.Now().Add(30 * time.Second)},
			want:  false,
		},
		{
			name:  "valid",
			cache: tokenCache{token: "x", expiresAt: time.Now().Add(5 * time.Minute)},
			want:  true,
		},
	}

	for _, c := range cases {
		if got := c.cache.valid(); got != c.want {
			t.Fatalf("%s: valid() = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestParseBigIntLargeValue(t *testing.T) {
	bigVal := new(big.Int).Lsh(big.NewInt(1), 62).String()
	if _, err := parseBigInt(bigVal); err != nil {
		t.Fatalf("unexpected error for large int64-range value: %v", err)
	}
}
