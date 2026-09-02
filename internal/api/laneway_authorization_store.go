package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/store"
)

func (a *API) saveLanewayState(ctx context.Context, state string, pending lanewayAuthorizationState) error {
	hash := lanewayStateHash(state)
	verifier, err := a.eventConfig.Vault.Encrypt("laneway-authorization:"+hash, []byte(pending.CodeVerifier))
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	return a.store.CreateLanewayAuthorizationTransaction(ctx, core.LanewayAuthorizationTransaction{
		StateHash: hash, Kind: pending.Kind, ConnectionName: pending.Name,
		Authority: pending.Authority, RedirectURI: pending.RedirectURI,
		ApplicationID: pending.ApplicationID, EncryptedCodeVerifier: verifier,
		ExpiresAt: pending.ExpiresAt, CreatedAt: now,
	})
}

func (a *API) consumeLanewayState(ctx context.Context, state string) (lanewayAuthorizationState, error) {
	var pending lanewayAuthorizationState
	if state == "" {
		return pending, store.ErrNotFound
	}
	transaction, err := a.store.ConsumeLanewayAuthorizationTransaction(ctx, lanewayStateHash(state), time.Now().UTC())
	if err != nil {
		return pending, err
	}
	verifier, err := a.eventConfig.Vault.Decrypt("laneway-authorization:"+transaction.StateHash, transaction.EncryptedCodeVerifier)
	if err != nil {
		return pending, err
	}
	return lanewayAuthorizationState{
		Kind: transaction.Kind, Name: transaction.ConnectionName,
		Authority: transaction.Authority, CodeVerifier: string(verifier),
		RedirectURI: transaction.RedirectURI, ApplicationID: transaction.ApplicationID,
		ExpiresAt: transaction.ExpiresAt,
	}, nil
}

func (a *API) reusableLanewayApplication(ctx context.Context, authority string) (core.LanewayApplication, bool, error) {
	item, err := a.store.GetActiveLanewayApplicationByAuthority(ctx, authority)
	if errors.Is(err, store.ErrNotFound) {
		return core.LanewayApplication{}, false, nil
	}
	return item, err == nil, err
}

func (a *API) lanewayApplication(ctx context.Context, id string) (core.LanewayApplication, string, error) {
	item, err := a.store.GetLanewayApplication(ctx, id)
	if err != nil {
		return item, "", err
	}
	secret, err := a.eventConfig.Vault.Decrypt("laneway-application:"+item.ID, item.EncryptedClientSecret)
	if err != nil {
		return item, "", err
	}
	return item, string(secret), nil
}

func lanewayStateHash(state string) string {
	hash := sha256.Sum256([]byte(state))
	return hex.EncodeToString(hash[:])
}
