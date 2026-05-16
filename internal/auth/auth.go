package auth

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	cfg "savras/internal/config"

	"github.com/go-ldap/ldap/v3"
	"github.com/golang-jwt/jwt/v5"
)

// AuthResult holds basic user information retrieved from LDAP
type AuthResult struct {
	Username string
	Email    string
	Groups   []string
}

// JWTClaims represents the data stored inside JWTs for this project
type JWTClaims struct {
	Username string   `json:"username"`
	Email    string   `json:"email"`
	Groups   []string `json:"groups"`
	jwt.RegisteredClaims
}

var (
	globalCfg  *cfg.Config
	privateKey *rsa.PrivateKey
)

// Init loads the configuration for the auth package
func Init(c *cfg.Config) {
	globalCfg = c
	// reset cached key when config changes
	privateKey = nil
}

// getPrivateKey lazily loads the RSA private key from config or env
func getPrivateKey() (*rsa.PrivateKey, error) {
	if privateKey != nil {
		return privateKey, nil
	}
	if globalCfg == nil {
		return nil, errors.New("auth: config not initialized")
	}
	keyPEM := strings.TrimSpace(globalCfg.Auth.JwtPrivateKey)
	if keyPEM == "" {
		keyPEM = strings.TrimSpace(os.Getenv("JWT_PRIVATE_KEY"))
	}
	if keyPEM == "" {
		return nil, errors.New("auth: no private key configured")
	}
	block, _ := pem.Decode([]byte(keyPEM))
	if block == nil {
		return nil, errors.New("auth: invalid private key PEM")
	}
	var parsed *rsa.PrivateKey
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		parsed = k
	} else if k2, err2 := x509.ParsePKCS8PrivateKey(block.Bytes); err2 == nil {
		if rsaKey, ok := k2.(*rsa.PrivateKey); ok {
			parsed = rsaKey
		} else {
			return nil, errors.New("auth: not RSA private key in PKCS#8")
		}
	} else {
		return nil, err
	}
	if parsed == nil {
		return nil, errors.New("auth: failed to parse private key")
	}
	privateKey = parsed
	return parsed, nil
}

// Authenticate performs LDAP bind validation for the given username/password.
// If a local admin user is configured, it checks those credentials first.
func Authenticate(username, password string) (*AuthResult, error) {
	if globalCfg == nil {
		return nil, errors.New("auth: config not initialized")
	}

	if globalCfg.Auth.LocalAdminUsername != "" && globalCfg.Auth.LocalAdminPassword != "" {
		if username == globalCfg.Auth.LocalAdminUsername && password == globalCfg.Auth.LocalAdminPassword {
			return &AuthResult{Username: username, Groups: []string{"Grafana_Admins"}}, nil
		}
	}

	conn, err := ldap.DialURL(globalCfg.LDAP.URL())
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	if globalCfg.LDAP.BindDN != "" {
		if err := conn.Bind(globalCfg.LDAP.BindDN, globalCfg.LDAP.BindPassword); err != nil {
			return nil, err
		}
	}

	attr := globalCfg.LDAP.UserAttr
	if attr == "" {
		attr = "sAMAccountName"
	}

	filter := fmt.Sprintf(globalCfg.LDAP.UserFilter, ldap.EscapeFilter(username))
	sr, err := conn.Search(ldap.NewSearchRequest(
		globalCfg.LDAP.BaseDN, ldap.ScopeWholeSubtree, ldap.NeverDerefAliases,
		0, 0, false, filter, []string{"dn", globalCfg.LDAP.EmailAttr, "memberOf"}, nil))
	if err != nil {
		return nil, err
	}
	if len(sr.Entries) == 0 {
		return nil, errors.New("auth: user not found")
	}
	userDN := sr.Entries[0].DN
	if err := conn.Bind(userDN, password); err != nil {
		return nil, err
	}
	email := sr.Entries[0].GetAttributeValue(globalCfg.LDAP.EmailAttr)
	groups := sr.Entries[0].GetAttributeValues("memberOf")
	return &AuthResult{Username: username, Email: email, Groups: groups}, nil
}

// GetUserInfo retrieves basic user information from LDAP without authenticating
func GetUserInfo(username string) (*AuthResult, error) {
	if globalCfg == nil {
		return nil, errors.New("auth: config not initialized")
	}
	conn, err := ldap.DialURL(globalCfg.LDAP.URL())
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if globalCfg.LDAP.BindDN != "" {
		if err := conn.Bind(globalCfg.LDAP.BindDN, globalCfg.LDAP.BindPassword); err != nil {
			return nil, err
		}
	}
	attr := globalCfg.LDAP.UserAttr
	if attr == "" {
		attr = "sAMAccountName"
	}
	filter := fmt.Sprintf(globalCfg.LDAP.UserFilter, ldap.EscapeFilter(username))
	sr, err := conn.Search(ldap.NewSearchRequest(
		globalCfg.LDAP.BaseDN, ldap.ScopeWholeSubtree, ldap.NeverDerefAliases,
		0, 0, false, filter, []string{"dn", globalCfg.LDAP.EmailAttr, "memberOf"}, nil))
	if err != nil {
		return nil, err
	}
	if len(sr.Entries) == 0 {
		return nil, errors.New("auth: user not found")
	}
	email := sr.Entries[0].GetAttributeValue(globalCfg.LDAP.EmailAttr)
	groups := sr.Entries[0].GetAttributeValues("memberOf")
	return &AuthResult{Username: username, Email: email, Groups: groups}, nil
}

// GenerateJWT creates a JWT for the given user using RSA (PKCS#1/PKCS#8) if available, otherwise HS256 with a secret
func GenerateJWT(user *AuthResult) (string, error) {
	if user == nil {
		return "", errors.New("auth: user is nil")
	}
	claims := JWTClaims{
		Username: user.Username,
		Email:    user.Email,
		Groups:   user.Groups,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "grafana-auth-proxy",
			Subject:   user.Username,
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(globalCfg.Auth.JwtExpiryDuration)),
		},
	}

	// Try RSA first
	if key, err := getPrivateKey(); err == nil && key != nil {
		token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
		return token.SignedString(key)
	}

	// Fallback to HMAC if configured
	if globalCfg.Auth.JwtSecret == "" {
		return "", errors.New("auth: no signing key configured")
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(globalCfg.Auth.JwtSecret))
}

// ValidateJWT validates a JWT and returns its claims
func ValidateJWT(tokenString string) (*JWTClaims, error) {
	if tokenString == "" {
		return nil, errors.New("auth: empty token")
	}
	// Try RSA first if key is available
	if key, err := getPrivateKey(); err == nil && key != nil {
		pub := &key.PublicKey
		t, err := jwt.ParseWithClaims(tokenString, &JWTClaims{}, func(tok *jwt.Token) (interface{}, error) {
			if _, ok := tok.Method.(*jwt.SigningMethodRSA); !ok {
				return nil, fmt.Errorf("unexpected signing method: %v", tok.Header["alg"])
			}
			return pub, nil
		})
		if err != nil {
			return nil, err
		}
		if claims, ok := t.Claims.(*JWTClaims); ok && t.Valid {
			return claims, nil
		}
		return nil, errors.New("auth: invalid token")
	}

	// Fallback to HS256
	if globalCfg == nil || globalCfg.Auth.JwtSecret == "" {
		return nil, errors.New("auth: no signing key configured")
	}
	t, err := jwt.ParseWithClaims(tokenString, &JWTClaims{}, func(tok *jwt.Token) (interface{}, error) {
		if _, ok := tok.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", tok.Header["alg"])
		}
		return []byte(globalCfg.Auth.JwtSecret), nil
	})
	if err != nil {
		return nil, err
	}
	if claims, ok := t.Claims.(*JWTClaims); ok && t.Valid {
		return claims, nil
	}
	return nil, errors.New("auth: invalid token")
}
