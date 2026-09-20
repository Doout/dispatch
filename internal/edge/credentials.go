package edge

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/store"
)

const EnrollmentLifetime = 15 * time.Minute
const SessionLifetime = 10 * time.Minute
const ChallengeLifetime = time.Minute

type CredentialStore interface {
	GetEdgeCredential(context.Context, string) (core.EdgeCredential, error)
	RotateEdgeCredential(context.Context, core.EdgeCredential) error
	EnrollEdgeCredential(context.Context, string, string, string, string, time.Time, time.Time) error
	CreateEdgeChallenge(context.Context, string, string, int64, time.Time, time.Time) error
	ExchangeEdgeChallenge(context.Context, string, string, string, string, int64, time.Time, time.Time) error
	RevokeEdgeCredential(context.Context, string, time.Time) error
}
type Session struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expiresAt"`
}
type Challenge struct {
	Challenge  string    `json:"challenge"`
	Generation int64     `json:"generation"`
	ExpiresAt  time.Time `json:"expiresAt"`
}

func TokenHash(token string) string {
	hash := sha256.Sum256([]byte(token))
	return base64.RawURLEncoding.EncodeToString(hash[:])
}
func randomToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
func RotateCredentials(ctx context.Context, data CredentialStore, id string, now time.Time) (string, error) {
	token, err := randomToken()
	if err != nil {
		return "", err
	}
	err = data.RotateEdgeCredential(ctx, core.EdgeCredential{NetworkID: id, EnrollmentHash: TokenHash(token), EnrollmentExpiresAt: now.Add(EnrollmentLifetime), UpdatedAt: now})
	return token, err
}
func Enroll(ctx context.Context, data CredentialStore, id, token, publicKey string, now time.Time) (Session, error) {
	key, err := base64.RawURLEncoding.DecodeString(publicKey)
	if err != nil || len(key) != ed25519.PublicKeySize || len(token) < 32 || len(token) > 128 {
		return Session{}, store.ErrEdgeCredential
	}
	session, err := randomToken()
	if err != nil {
		return Session{}, err
	}
	expires := now.Add(SessionLifetime)
	if err = data.EnrollEdgeCredential(ctx, id, TokenHash(token), publicKey, TokenHash(session), now, expires); err != nil {
		return Session{}, err
	}
	return Session{Token: session, ExpiresAt: expires}, nil
}
func NewChallenge(ctx context.Context, data CredentialStore, id, publicKey string, now time.Time) (Challenge, error) {
	c, err := data.GetEdgeCredential(ctx, id)
	if err != nil || c.Revoked || c.PublicKey == "" || subtle.ConstantTimeCompare([]byte(c.PublicKey), []byte(publicKey)) != 1 {
		return Challenge{}, store.ErrEdgeCredential
	}
	challenge, err := randomToken()
	if err != nil {
		return Challenge{}, err
	}
	expires := now.Add(ChallengeLifetime)
	err = data.CreateEdgeChallenge(ctx, id, TokenHash(challenge), c.Generation, now, expires)
	return Challenge{Challenge: challenge, Generation: c.Generation, ExpiresAt: expires}, err
}
func ChallengeMessage(id, challenge string, generation int64) []byte {
	return []byte(fmt.Sprintf("dispatch-edge-session-v1\n%s\n%d\n%s", id, generation, challenge))
}
func Exchange(ctx context.Context, data CredentialStore, id, challenge, signature string, generation int64, now time.Time) (Session, error) {
	if len(challenge) > 128 || len(signature) > 128 {
		return Session{}, store.ErrEdgeCredential
	}
	c, err := data.GetEdgeCredential(ctx, id)
	if err != nil || c.Revoked || c.Generation != generation {
		return Session{}, store.ErrEdgeCredential
	}
	key, err := base64.RawURLEncoding.DecodeString(c.PublicKey)
	if err != nil || len(key) != ed25519.PublicKeySize {
		return Session{}, store.ErrEdgeCredential
	}
	sig, err := base64.RawURLEncoding.DecodeString(signature)
	if err != nil || !ed25519.Verify(key, ChallengeMessage(id, challenge, generation), sig) {
		return Session{}, store.ErrEdgeCredential
	}
	token, err := randomToken()
	if err != nil {
		return Session{}, err
	}
	expires := now.Add(SessionLifetime)
	err = data.ExchangeEdgeChallenge(ctx, id, TokenHash(challenge), c.PublicKey, TokenHash(token), generation, now, expires)
	return Session{Token: token, ExpiresAt: expires}, err
}
func AuthenticateSession(ctx context.Context, data CredentialStore, id, token string, now time.Time) (bool, bool) {
	c, err := data.GetEdgeCredential(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return false, false
	}
	if err != nil || c.Revoked || c.SessionHash == "" || !c.SessionExpiresAt.After(now) || len(token) > 128 {
		return false, true
	}
	return subtle.ConstantTimeCompare([]byte(c.SessionHash), []byte(TokenHash(token))) == 1, true
}
func KeyFingerprint(publicKey string) string {
	sum := sha256.Sum256([]byte(publicKey))
	return base64.RawURLEncoding.EncodeToString(sum[:12])
}
