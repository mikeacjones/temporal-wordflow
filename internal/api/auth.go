package api

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	passwordIterations = 600_000
	sessionCookieName  = "wordflow_session"
	sessionIssuer      = "temporal-wordflow"
	sessionLifetime    = 30 * 24 * time.Hour
	jwtHeader          = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9"
)

type sessionClaims struct {
	Issuer    string `json:"iss"`
	Subject   string `json:"sub"`
	IssuedAt  int64  `json:"iat"`
	ExpiresAt int64  `json:"exp"`
}

func normalizeUsername(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) < 3 || len(value) > 32 {
		return "", errors.New("username must be between 3 and 32 characters")
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') ||
			character == '-' || character == '_' {
			continue
		}
		return "", errors.New("username may contain letters, numbers, hyphens, and underscores")
	}
	return value, nil
}

func validateDisplayName(value string) (string, error) {
	value = strings.TrimSpace(value)
	length := utf8.RuneCountInString(value)
	if length < 2 || length > 24 {
		return "", errors.New("display name must be between 2 and 24 characters")
	}
	return value, nil
}

func taggedDisplayName(username, displayName string) string {
	digest := sha256.Sum256([]byte("temporal-wordflow/display-name/" + username))
	return fmt.Sprintf("%s#%s", displayName, hex.EncodeToString(digest[:4]))
}

func passwordHash(username, password string) (string, error) {
	if len(password) < 8 || len(password) > 128 {
		return "", errors.New("password must be between 8 and 128 characters")
	}
	salt := sha256.Sum256([]byte("temporal-wordflow/account/" + username))
	hash, err := pbkdf2.Key(sha256.New, password, salt[:], passwordIterations, 32)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("pbkdf2-sha256$%d$%s", passwordIterations, hex.EncodeToString(hash)), nil
}

func newSessionToken(secret []byte, playerID string, issuedAt, expiresAt time.Time) (string, error) {
	claims, err := json.Marshal(sessionClaims{
		Issuer: sessionIssuer, Subject: playerID,
		IssuedAt: issuedAt.Unix(), ExpiresAt: expiresAt.Unix(),
	})
	if err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(claims)
	unsigned := jwtHeader + "." + payload
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(unsigned))
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func setSessionCookie(writer http.ResponseWriter, request *http.Request, token string, expiresAt time.Time) {
	http.SetCookie(writer, &http.Cookie{
		Name: sessionCookieName, Value: token,
		Path: "/", Expires: expiresAt, MaxAge: int(time.Until(expiresAt).Seconds()),
		HttpOnly: true, Secure: request.TLS != nil || request.Header.Get("X-Forwarded-Proto") == "https",
		SameSite: http.SameSiteLaxMode,
	})
}

func clearSessionCookie(writer http.ResponseWriter, request *http.Request) {
	setSessionCookie(writer, request, "", time.Unix(1, 0))
}

func sessionCookie(request *http.Request, secret []byte, now time.Time) (string, error) {
	cookie, err := request.Cookie(sessionCookieName)
	if err != nil {
		return "", errors.New("not signed in")
	}
	parts := strings.Split(cookie.Value, ".")
	if len(parts) != 3 || parts[0] != jwtHeader {
		return "", errors.New("invalid session")
	}
	unsigned := parts[0] + "." + parts[1]
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return "", errors.New("invalid session")
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(unsigned))
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return "", errors.New("invalid session")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", errors.New("invalid session")
	}
	var claims sessionClaims
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Issuer != sessionIssuer ||
		!validUsername(claims.Subject) || claims.IssuedAt > now.Add(time.Minute).Unix() || now.Unix() >= claims.ExpiresAt {
		return "", errors.New("invalid session")
	}
	return claims.Subject, nil
}

func validUsername(value string) bool {
	normalized, err := normalizeUsername(value)
	return err == nil && normalized == value
}
