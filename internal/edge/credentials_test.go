package edge

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/store"
)

func TestEdgeCredentialLifecycle(t *testing.T) {
	testCredentialLifecycle(t, filepath.Join(t.TempDir(), "edge.db"))
}
func TestEdgeCredentialPostgresIntegration(t *testing.T) {
	dsn := os.Getenv("DISPATCH_EDGE_POSTGRES_URL")
	if dsn == "" {
		t.Skip("set DISPATCH_EDGE_POSTGRES_URL to a disposable database")
	}
	testCredentialLifecycle(t, dsn)
}
func testCredentialLifecycle(t *testing.T, dsn string) {
	ctx := context.Background()
	data, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer data.Close()
	if err = data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	id := "edge-lifecycle"
	if err = data.CreatePrivateNetwork(ctx, core.PrivateNetwork{ID: id, Name: id, Driver: DriverAgent, State: "waiting", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	token, err := RotateCredentials(ctx, data, id, now)
	if err != nil {
		t.Fatal(err)
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(public)
	if ok, managed := AuthenticateSession(ctx, data, id, token, now); ok || !managed {
		t.Fatal("enrollment used as runtime session")
	}
	if _, err = Enroll(ctx, data, id, token, encoded, now.Add(EnrollmentLifetime)); !errors.Is(err, store.ErrEdgeCredential) {
		t.Fatal("expired enrollment accepted", err)
	}
	session, err := Enroll(ctx, data, id, token, encoded, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Enroll(ctx, data, id, token, encoded, now); !errors.Is(err, store.ErrEdgeCredential) {
		t.Fatal("enrollment replay accepted", err)
	}
	if ok, _ := AuthenticateSession(ctx, data, id, session.Token, now); !ok {
		t.Fatal("session rejected")
	}
	if ok, _ := AuthenticateSession(ctx, data, id, session.Token, session.ExpiresAt); ok {
		t.Fatal("expired session accepted")
	}
	challenge, err := NewChallenge(ctx, data, id, encoded, now)
	if err != nil {
		t.Fatal(err)
	}
	signature := base64.RawURLEncoding.EncodeToString(ed25519.Sign(private, ChallengeMessage(id, challenge.Challenge, challenge.Generation)))
	if _, err = Exchange(ctx, data, id, challenge.Challenge, signature, challenge.Generation, challenge.ExpiresAt); !errors.Is(err, store.ErrEdgeCredential) {
		t.Fatal("expired challenge accepted", err)
	}
	// Each exchange consumes its challenge atomically, including concurrent replay.
	challenge, err = NewChallenge(ctx, data, id, encoded, now)
	if err != nil {
		t.Fatal(err)
	}
	signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(private, ChallengeMessage(id, challenge.Challenge, challenge.Generation)))
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, e := Exchange(ctx, data, id, challenge.Challenge, signature, challenge.Generation, now); e == nil {
				accepted.Add(1)
			}
		}()
	}
	wg.Wait()
	if accepted.Load() != 1 {
		t.Fatal("challenge replay winners", accepted.Load())
	}
	if ok, _ := AuthenticateSession(ctx, data, id, session.Token, now); ok {
		t.Fatal("old session remained valid after renewal")
	}
	challenge, err = NewChallenge(ctx, data, id, encoded, now)
	if err != nil {
		t.Fatal(err)
	}
	signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(private, ChallengeMessage("other-node", challenge.Challenge, challenge.Generation)))
	if _, err = Exchange(ctx, data, id, challenge.Challenge, signature, challenge.Generation, now); !errors.Is(err, store.ErrEdgeCredential) {
		t.Fatal("cross-node signature accepted", err)
	}
	if err = data.RevokeEdgeCredential(ctx, id, now); err != nil {
		t.Fatal(err)
	}
	if _, err = NewChallenge(ctx, data, id, encoded, now); !errors.Is(err, store.ErrEdgeCredential) {
		t.Fatal("revoked identity challenged", err)
	}
	token, err = RotateCredentials(ctx, data, id, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Enroll(ctx, data, id, token, encoded, now); err != nil {
		t.Fatal("cannot re-enroll", err)
	}
	if err = data.DeletePrivateNetwork(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err = data.GetEdgeCredential(ctx, id); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("identity not deleted", err)
	}
}
