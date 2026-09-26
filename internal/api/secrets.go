package api

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/go-chi/chi/v5"
	"github.com/oklog/ulid/v2"
	"golang.org/x/crypto/ssh"
)

type secretRequest struct {
	Name                string  `json:"name"`
	Type                string  `json:"type"`
	Source              string  `json:"source"`
	EnvironmentVariable string  `json:"environmentVariable"`
	Value               *string `json:"value"`
	Generate            bool    `json:"generate"`
	ExternalStoreID     string  `json:"externalStoreId"`
	ExternalSecretID    string  `json:"externalSecretId"`
	ExternalField       string  `json:"externalField"`
}

var environmentVariablePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

func validateSecretInput(name string, secretType core.SecretType, environmentVariable string, value *string, requireValue bool) string {
	if name == "" || len(name) > 80 {
		return "Enter a name no longer than 80 characters."
	}
	if !core.ValidSecretType(secretType) {
		return "Choose a supported secret type."
	}
	if !environmentVariablePattern.MatchString(environmentVariable) {
		return "Use a valid environment variable name."
	}
	if strings.HasPrefix(environmentVariable, "DISPATCH_") {
		return "DISPATCH_ variables are reserved by the controller."
	}
	if requireValue && (value == nil || *value == "") {
		return "Enter a secret value."
	}
	if value != nil && len(*value) > 64<<10 {
		return "Keep the secret value under 64 KiB."
	}
	if value != nil && *value != "" && secretType == core.SecretTypeSSHPrivateKey {
		if _, err := sshPublicKey(*value); err != nil {
			return err.Error()
		}
	}
	return ""
}

func normalizeSecretType(value string) core.SecretType {
	if strings.TrimSpace(value) == "" {
		return core.SecretTypeText
	}
	return core.SecretType(strings.TrimSpace(value))
}

func normalizeSecretSource(value string) core.SecretSource {
	if strings.TrimSpace(value) == "" {
		return core.SecretSourceLocal
	}
	return core.SecretSource(strings.TrimSpace(value))
}

func (a *API) validateExternalSecret(ctx context.Context, input secretRequest) string {
	if strings.TrimSpace(input.ExternalStoreID) == "" {
		return "Choose a secret store."
	}
	if strings.TrimSpace(input.ExternalSecretID) == "" || len(input.ExternalSecretID) > 2048 {
		return "Enter the secret ID from the provider."
	}
	if len(input.ExternalField) > 256 {
		return "Keep the value field under 256 characters."
	}
	item, err := a.store.GetSecretStore(ctx, strings.TrimSpace(input.ExternalStoreID))
	if err != nil {
		return "Choose an available secret store."
	}
	if item.State != "ready" {
		return "Verify the secret store before using it."
	}
	return ""
}

func generateSSHKey() (privateKey, publicKey string, err error) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	block, err := ssh.MarshalPrivateKey(private, "dispatch")
	if err != nil {
		return "", "", err
	}
	public, err := ssh.NewPublicKey(private.Public())
	if err != nil {
		return "", "", err
	}
	return string(pem.EncodeToMemory(block)), strings.TrimSpace(string(ssh.MarshalAuthorizedKey(public))), nil
}

func sshPublicKey(privateKey string) (string, error) {
	parsed, err := ssh.ParseRawPrivateKey([]byte(strings.TrimSpace(privateKey)))
	if err != nil {
		var missing *ssh.PassphraseMissingError
		if errors.As(err, &missing) {
			return "", errors.New("Use an unencrypted SSH private key so deployments can run without a passphrase")
		}
		return "", errors.New("Enter a valid OpenSSH or PEM private key")
	}
	signer, ok := parsed.(crypto.Signer)
	if !ok {
		return "", errors.New("The SSH private key uses an unsupported format")
	}
	public, err := ssh.NewPublicKey(signer.Public())
	if err != nil {
		return "", errors.New("Could not derive the SSH public key")
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(public))), nil
}

func (a *API) listSecrets(w http.ResponseWriter, r *http.Request) {
	items, err := a.store.ListSecrets(r.Context())
	a.list(w, items, err)
}

func (a *API) createSecret(w http.ResponseWriter, r *http.Request) {
	if a.eventConfig.Vault == nil {
		problem(w, http.StatusServiceUnavailable, "Secret storage is not configured", "Set DISPATCH_MASTER_KEY_FILE before saving credentials.")
		return
	}
	var input secretRequest
	if !decode(w, r, &input) {
		return
	}
	input.Name, input.EnvironmentVariable = strings.TrimSpace(input.Name), strings.TrimSpace(input.EnvironmentVariable)
	secretType := normalizeSecretType(input.Type)
	secretSource := normalizeSecretSource(input.Source)
	if input.Generate {
		if secretSource != core.SecretSourceLocal || secretType != core.SecretTypeSSHPrivateKey {
			problem(w, http.StatusBadRequest, "Invalid secret", "Only SSH private keys can be generated.")
			return
		}
		privateKey, _, err := generateSSHKey()
		if err != nil {
			a.internal(w, err)
			return
		}
		input.Value = &privateKey
	}
	if secretSource != core.SecretSourceLocal && secretSource != core.SecretSourceExternal {
		problem(w, http.StatusBadRequest, "Invalid secret", "Choose local or external storage.")
		return
	}
	if detail := validateSecretInput(input.Name, secretType, input.EnvironmentVariable, input.Value, secretSource == core.SecretSourceLocal); detail != "" {
		problem(w, http.StatusBadRequest, "Invalid secret", detail)
		return
	}
	if secretSource == core.SecretSourceExternal {
		if input.Value != nil && *input.Value != "" {
			problem(w, http.StatusBadRequest, "Invalid secret", "External secret references do not accept a local value.")
			return
		}
		if detail := a.validateExternalSecret(r.Context(), input); detail != "" {
			problem(w, http.StatusBadRequest, "Invalid secret", detail)
			return
		}
	}
	now := time.Now().UTC()
	item := core.Secret{ID: ulid.Make().String(), Name: input.Name, Type: secretType, Source: secretSource, EnvironmentVariable: input.EnvironmentVariable,
		ExternalStoreID: strings.TrimSpace(input.ExternalStoreID), ExternalSecretID: strings.TrimSpace(input.ExternalSecretID), ExternalField: strings.TrimSpace(input.ExternalField), CreatedAt: now, UpdatedAt: now}
	if item.Source == core.SecretSourceLocal && item.Type == core.SecretTypeSSHPrivateKey {
		item.PublicValue, _ = sshPublicKey(*input.Value)
	}
	if item.Source == core.SecretSourceLocal {
		encrypted, err := a.eventConfig.Vault.Encrypt("secret:"+item.ID, []byte(*input.Value))
		if err != nil {
			a.internal(w, err)
			return
		}
		item.EncryptedValue = encrypted
	} else {
		item.EncryptedValue = ""
	}
	if err := a.store.CreateSecret(r.Context(), item); err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

func (a *API) updateSecret(w http.ResponseWriter, r *http.Request) {
	if a.eventConfig.Vault == nil {
		problem(w, http.StatusServiceUnavailable, "Secret storage is not configured", "Set DISPATCH_MASTER_KEY_FILE before saving credentials.")
		return
	}
	item, err := a.store.GetSecret(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.notFoundOrInternal(w, err, "Secret")
		return
	}
	var input secretRequest
	if !decode(w, r, &input) {
		return
	}
	input.Name, input.EnvironmentVariable = strings.TrimSpace(input.Name), strings.TrimSpace(input.EnvironmentVariable)
	secretType := item.Type
	if strings.TrimSpace(input.Type) != "" {
		secretType = normalizeSecretType(input.Type)
	}
	secretSource := item.Source
	if secretSource == "" {
		secretSource = core.SecretSourceLocal
	}
	if strings.TrimSpace(input.Source) != "" {
		secretSource = normalizeSecretSource(input.Source)
	}
	if input.Generate {
		if secretSource != core.SecretSourceLocal || secretType != core.SecretTypeSSHPrivateKey {
			problem(w, http.StatusBadRequest, "Invalid secret", "Only SSH private keys can be generated.")
			return
		}
		privateKey, _, generateErr := generateSSHKey()
		if generateErr != nil {
			a.internal(w, generateErr)
			return
		}
		input.Value = &privateKey
	}
	if secretSource != core.SecretSourceLocal && secretSource != core.SecretSourceExternal {
		problem(w, http.StatusBadRequest, "Invalid secret", "Choose local or external storage.")
		return
	}
	validationValue := input.Value
	if secretSource == core.SecretSourceLocal && (validationValue == nil || *validationValue == "") && (secretType != item.Type || item.Source == core.SecretSourceExternal) {
		if item.Source == core.SecretSourceExternal {
			problem(w, http.StatusBadRequest, "Invalid secret", "Enter a value when moving an external secret into Dispatch.")
			return
		}
		plaintext, decryptErr := a.secretResolver.Resolve(r.Context(), item.ID)
		if decryptErr != nil {
			a.internal(w, decryptErr)
			return
		}
		current := string(plaintext)
		validationValue = &current
	}
	if detail := validateSecretInput(input.Name, secretType, input.EnvironmentVariable, validationValue, false); detail != "" {
		problem(w, http.StatusBadRequest, "Invalid secret", detail)
		return
	}
	if secretSource == core.SecretSourceExternal {
		if detail := a.validateExternalSecret(r.Context(), input); detail != "" {
			problem(w, http.StatusBadRequest, "Invalid secret", detail)
			return
		}
	}
	item.Name, item.Type, item.Source, item.EnvironmentVariable, item.UpdatedAt = input.Name, secretType, secretSource, input.EnvironmentVariable, time.Now().UTC()
	if secretSource == core.SecretSourceLocal && input.Value != nil && *input.Value != "" {
		item.EncryptedValue, err = a.eventConfig.Vault.Encrypt("secret:"+item.ID, []byte(*input.Value))
		if err != nil {
			a.internal(w, err)
			return
		}
	}
	if secretSource == core.SecretSourceExternal {
		item.EncryptedValue, item.PublicValue = "", ""
		item.ExternalStoreID, item.ExternalSecretID, item.ExternalField = strings.TrimSpace(input.ExternalStoreID), strings.TrimSpace(input.ExternalSecretID), strings.TrimSpace(input.ExternalField)
	} else if item.Type == core.SecretTypeSSHPrivateKey {
		value := validationValue
		if input.Value != nil && *input.Value != "" {
			value = input.Value
		}
		if value != nil {
			item.PublicValue, _ = sshPublicKey(*value)
		}
	} else {
		item.PublicValue = ""
	}
	if secretSource == core.SecretSourceLocal {
		item.ExternalStoreID, item.ExternalSecretID, item.ExternalField = "", "", ""
	}
	if err := a.store.UpdateSecret(r.Context(), item); err != nil {
		a.notFoundOrInternal(w, err, "Secret")
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (a *API) deleteSecret(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	apps, err := a.store.ListApps(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	for _, app := range apps {
		if app.SourceCredentialID == id {
			problem(w, http.StatusConflict, "Credential in use", "Remove this credential from the application source before deleting it.")
			return
		}
		if slices.Contains(app.HookSecretIDs, id) {
			problem(w, http.StatusConflict, "Credential in use", "Detach this credential from the application build hook before deleting it.")
			return
		}
	}
	triggers, err := a.store.ListEventTriggers(r.Context(), "")
	if err != nil {
		a.internal(w, err)
		return
	}
	for _, trigger := range triggers {
		if slices.Contains(trigger.SecretIDs, id) {
			problem(w, http.StatusConflict, "Credential in use", "Detach this credential from the event build hook before deleting it.")
			return
		}
	}
	groups, err := a.store.ListPreviewGroups(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	for _, group := range groups {
		for _, component := range group.Components {
			if slices.Contains(component.SecretIDs, id) {
				problem(w, http.StatusConflict, "Credential in use", "Detach this credential from the preview group build hook before deleting it.")
				return
			}
		}
	}
	if err := a.store.DeleteSecret(r.Context(), id); err != nil {
		a.notFoundOrInternal(w, err, "Secret")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
