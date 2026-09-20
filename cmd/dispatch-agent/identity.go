package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type edgeIdentity struct {
	Controller string `json:"controller"`
	NodeID     string `json:"nodeId"`
	PrivateKey string `json:"privateKey"`
}

func loadIdentity(path, controller, node string) (edgeIdentity, error) {
	info, err := os.Lstat(path)
	if err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 4096 {
			return edgeIdentity{}, errors.New("Edge identity must be a private regular file with mode 0600")
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return edgeIdentity{}, errors.New("Cannot read the saved edge identity")
		}
		defer clear(raw)
		var identity edgeIdentity
		if json.Unmarshal(raw, &identity) != nil || identity.Controller != controller || identity.NodeID != node {
			return edgeIdentity{}, errors.New("Saved edge identity belongs to another controller or node")
		}
		if _, err = identity.key(); err != nil {
			return edgeIdentity{}, err
		}
		return identity, nil
	}
	if !os.IsNotExist(err) {
		return edgeIdentity{}, errors.New("Cannot inspect the saved edge identity")
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return edgeIdentity{}, errors.New("Cannot create the edge identity directory")
	}
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return edgeIdentity{}, errors.New("Cannot generate an edge identity")
	}
	defer clear(private)
	identity := edgeIdentity{Controller: controller, NodeID: node, PrivateKey: base64.RawURLEncoding.EncodeToString(private)}
	raw, _ := json.Marshal(identity)
	defer clear(raw)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return edgeIdentity{}, errors.New("Cannot create the edge identity file")
	}
	if _, err = file.Write(raw); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		return edgeIdentity{}, errors.New("Cannot persist the edge identity")
	}
	return identity, nil
}
func (i edgeIdentity) key() (ed25519.PrivateKey, error) {
	key, err := base64.RawURLEncoding.DecodeString(i.PrivateKey)
	if err != nil || len(key) != ed25519.PrivateKeySize {
		return nil, errors.New("Saved edge identity is invalid")
	}
	derived := ed25519.NewKeyFromSeed(key[:ed25519.SeedSize])
	if !bytes.Equal(derived, key) {
		return nil, errors.New("Saved edge identity is invalid")
	}
	return ed25519.PrivateKey(key), nil
}

type edgeSession struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expiresAt"`
}
type edgeChallenge struct {
	Challenge  string    `json:"challenge"`
	Generation int64     `json:"generation"`
	ExpiresAt  time.Time `json:"expiresAt"`
}

func obtainSession(ctx context.Context, client *http.Client, identity edgeIdentity, enrollment string) (edgeSession, error) {
	key, err := identity.key()
	if err != nil {
		return edgeSession{}, err
	}
	defer clear(key)
	public := base64.RawURLEncoding.EncodeToString(key.Public().(ed25519.PublicKey))
	base := identity.Controller + "/api/v1/edge/nodes/" + url.PathEscape(identity.NodeID)
	var challenge edgeChallenge
	err = identityRequest(ctx, client, base+"/challenge", "", map[string]string{"publicKey": public}, &challenge)
	if err != nil {
		if enrollment == "" {
			return edgeSession{}, errors.New("Node identity was refused; a new enrollment token may be required")
		}
		var session edgeSession
		if err = identityRequest(ctx, client, base+"/enroll", enrollment, map[string]string{"publicKey": public}, &session); err != nil {
			return edgeSession{}, errors.New("Node enrollment was refused or the controller is unavailable")
		}
		if err = validateSession(session); err != nil {
			return edgeSession{}, err
		}
		return session, nil
	}
	if len(challenge.Challenge) < 32 || len(challenge.Challenge) > 128 || challenge.Generation < 1 || !challenge.ExpiresAt.After(time.Now()) {
		return edgeSession{}, errors.New("Controller returned an invalid identity challenge")
	}
	message := []byte(fmt.Sprintf("dispatch-edge-session-v1\n%s\n%d\n%s", identity.NodeID, challenge.Generation, challenge.Challenge))
	signature := base64.RawURLEncoding.EncodeToString(ed25519.Sign(key, message))
	var session edgeSession
	err = identityRequest(ctx, client, base+"/session", "", map[string]any{"challenge": challenge.Challenge, "generation": challenge.Generation, "signature": signature}, &session)
	if err != nil {
		return edgeSession{}, errors.New("Controller refused the signed identity challenge")
	}
	if err = validateSession(session); err != nil {
		return edgeSession{}, err
	}
	return session, nil
}
func validateSession(s edgeSession) error {
	if len(s.Token) < 32 || len(s.Token) > 128 || !s.ExpiresAt.After(time.Now()) || s.ExpiresAt.After(time.Now().Add(15*time.Minute)) {
		return errors.New("Controller returned an invalid or excessive session lifetime")
	}
	return nil
}
func identityRequest(ctx context.Context, client *http.Client, target, token string, input, output any) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	raw, err := json.Marshal(input)
	if err != nil {
		return errors.New("Cannot prepare node identity request")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(raw))
	if err != nil {
		return errors.New("Cannot prepare node identity request")
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := client.Do(req)
	if err != nil {
		return errors.New("Controller identity request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("Controller identity request returned HTTP %d", response.StatusCode)
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 4096))
	if err = decoder.Decode(output); err != nil {
		return errors.New("Controller identity response is invalid")
	}
	return nil
}
func validEdgeMethod(method string) bool {
	switch strings.ToUpper(method) {
	case "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS":
		return method == strings.ToUpper(method)
	default:
		return false
	}
}
