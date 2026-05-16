package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"testing"
	"time"

	"savras/internal/config"
)

func TestInit(t *testing.T) {
	cfg := &config.Config{
		Auth: config.AuthConfig{
			JwtExpiryDuration: 1 * time.Hour,
		},
	}
	Init(cfg)
	if globalCfg != cfg {
		t.Error("expected globalCfg to be set")
	}
}

func TestGenerateAndValidateJWT(t *testing.T) {
	cfg := &config.Config{
		Auth: config.AuthConfig{
			JwtSecret:        "test-secret",
			JwtExpiryDuration: 1 * time.Hour,
		},
	}
	Init(cfg)

	user := &AuthResult{
		Username: "testuser",
		Email:    "test@example.com",
		Groups:   []string{"group1"},
	}

	token, err := GenerateJWT(user)
	if err != nil {
		t.Fatalf("failed to generate JWT: %v", err)
	}
	if token == "" {
		t.Fatal("expected non-empty token")
	}

	claims, err := ValidateJWT(token)
	if err != nil {
		t.Fatalf("failed to validate JWT: %v", err)
	}
	if claims.Username != "testuser" {
		t.Errorf("expected testuser, got %s", claims.Username)
	}
	if claims.Email != "test@example.com" {
		t.Errorf("expected test@example.com, got %s", claims.Email)
	}
}

func generateTestRSAPEM(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatalf("failed to generate RSA key: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	return string(pemBytes)
}

func generateTestPKCS8PEM(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatalf("failed to generate RSA key: %v", err)
	}
	keyBytes, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("failed to marshal PKCS#8: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: keyBytes,
	})
	return string(pemBytes)
}

func TestGetPrivateKey_PKCS1(t *testing.T) {
	pemStr := generateTestRSAPEM(t)
	cfg := &config.Config{
		Auth: config.AuthConfig{
			JwtPrivateKey: pemStr,
		},
	}
	Init(cfg)
	privateKey = nil

	key, err := getPrivateKey()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key == nil {
		t.Fatal("expected non-nil key")
	}
}

func TestGetPrivateKey_PKCS8(t *testing.T) {
	pemStr := generateTestPKCS8PEM(t)
	cfg := &config.Config{
		Auth: config.AuthConfig{
			JwtPrivateKey: pemStr,
		},
	}
	Init(cfg)
	privateKey = nil

	key, err := getPrivateKey()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key == nil {
		t.Fatal("expected non-nil key")
	}
}

func TestGetPrivateKey_InvalidPEM(t *testing.T) {
	cfg := &config.Config{
		Auth: config.AuthConfig{
			JwtPrivateKey: "invalid-pem",
		},
	}
	Init(cfg)
	privateKey = nil

	_, err := getPrivateKey()
	if err == nil {
		t.Fatal("expected error for invalid PEM")
	}
}

func TestGetPrivateKey_EmptyKey(t *testing.T) {
	cfg := &config.Config{
		Auth: config.AuthConfig{
			JwtPrivateKey: "",
		},
	}
	Init(cfg)
	privateKey = nil

	_, err := getPrivateKey()
	if err == nil {
		t.Fatal("expected error for empty key")
	}
}

func TestGetPrivateKey_Caching(t *testing.T) {
	pemStr := generateTestRSAPEM(t)
	cfg := &config.Config{
		Auth: config.AuthConfig{
			JwtPrivateKey: pemStr,
		},
	}
	Init(cfg)
	privateKey = nil

	key1, err := getPrivateKey()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	key2, err := getPrivateKey()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if key1 != key2 {
		t.Fatal("expected same key from cache")
	}
}

func TestGenerateJWT_NilUser(t *testing.T) {
	cfg := &config.Config{
		Auth: config.AuthConfig{
			JwtSecret:        "test-secret",
			JwtExpiryDuration: 1 * time.Hour,
		},
	}
	Init(cfg)

	_, err := GenerateJWT(nil)
	if err == nil {
		t.Fatal("expected error for nil user")
	}
}

func TestGenerateJWT_RSAKey(t *testing.T) {
	pemStr := generateTestRSAPEM(t)
	cfg := &config.Config{
		Auth: config.AuthConfig{
			JwtPrivateKey:     pemStr,
			JwtExpiryDuration: 1 * time.Hour,
		},
	}
	Init(cfg)

	user := &AuthResult{
		Username: "testuser",
		Email:    "test@example.com",
	}
	token, err := GenerateJWT(user)
	if err != nil {
		t.Fatalf("failed to generate JWT with RSA: %v", err)
	}
	if token == "" {
		t.Fatal("expected non-empty token")
	}

	claims, err := ValidateJWT(token)
	if err != nil {
		t.Fatalf("failed to validate RSA JWT: %v", err)
	}
	if claims.Username != "testuser" {
		t.Errorf("expected testuser, got %s", claims.Username)
	}
}

func TestGenerateJWT_NoKey(t *testing.T) {
	cfg := &config.Config{
		Auth: config.AuthConfig{
			JwtExpiryDuration: 1 * time.Hour,
		},
	}
	Init(cfg)
	privateKey = nil

	user := &AuthResult{
		Username: "testuser",
		Email:    "test@example.com",
	}
	_, err := GenerateJWT(user)
	if err == nil {
		t.Fatal("expected error when no signing key configured")
	}
}

func TestValidateJWT_EmptyToken(t *testing.T) {
	_, err := ValidateJWT("")
	if err == nil {
		t.Fatal("expected error for empty token")
	}
}

func TestValidateJWT_InvalidToken(t *testing.T) {
	cfg := &config.Config{
		Auth: config.AuthConfig{
			JwtSecret: "test-secret",
		},
	}
	Init(cfg)

	_, err := ValidateJWT("invalid-token")
	if err == nil {
		t.Fatal("expected error for invalid token")
	}
}

func TestValidateJWT_WrongSigningMethod(t *testing.T) {
	cfg := &config.Config{
		Auth: config.AuthConfig{
			JwtSecret: "test-secret",
		},
	}
	Init(cfg)

	wrongToken := "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJ1c2VybmFtZSI6InRlc3QifQ.wrong"
	_, err := ValidateJWT(wrongToken)
	if err == nil {
		t.Fatal("expected error for wrong signing method")
	}
}

func TestAuthenticate_NotInitialized(t *testing.T) {
	orig := globalCfg
	defer func() { globalCfg = orig }()
	globalCfg = nil

	_, err := Authenticate("user", "pass")
	if err == nil {
		t.Fatal("expected error when not initialized")
	}
	if err.Error() != "auth: config not initialized" {
		t.Fatalf("expected 'auth: config not initialized', got %q", err.Error())
	}
}

func TestGetUserInfo_NotInitialized(t *testing.T) {
	orig := globalCfg
	defer func() { globalCfg = orig }()
	globalCfg = nil

	_, err := GetUserInfo("user")
	if err == nil {
		t.Fatal("expected error when not initialized")
	}
	if err.Error() != "auth: config not initialized" {
		t.Fatalf("expected 'auth: config not initialized', got %q", err.Error())
	}
}
