package drift

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"math"
	"strconv"

	"github.com/doout/dispatch/internal/core"
	secretcrypto "github.com/doout/dispatch/internal/crypto"
)

// BaselineReader is the read-only evidence needed to group recorded resources.
type BaselineReader interface {
	GetDriftBaseline(context.Context, string) (core.DriftBaseline, error)
}

// RecordedResourceRepeats links older successful deployments to their immediate
// newer neighbor when their complete saved resources match. Items must be in
// newest-first history order; previous is the authorized pagination cursor.
// This reads existing encrypted evidence only. It does not recover baselines,
// contact targets, or imply that hooks and other deployment effects were equal.
func RecordedResourceRepeats(ctx context.Context, data BaselineReader, vault *secretcrypto.Vault, previous *core.Deployment, items []core.Deployment) map[string]string {
	repeats := map[string]string{}
	if data == nil || vault == nil {
		return repeats
	}
	var prior recordedResources
	if previous != nil {
		prior = readRecordedResources(ctx, data, vault, *previous)
	}
	for _, item := range items {
		if ctx.Err() != nil {
			break
		}
		current := readRecordedResources(ctx, data, vault, item)
		if prior.valid && current.valid && prior.deployment != current.deployment && prior.app == current.app && prior.server == current.server && prior.namespace == current.namespace && prior.release == current.release && prior.digest == current.digest {
			repeats[current.deployment] = prior.deployment
		}
		// Missing evidence and unsuccessful runs are barriers, never skipped.
		prior = current
	}
	return repeats
}

type recordedResources struct {
	deployment, app, server, namespace, release string
	digest                                      [sha256.Size]byte
	valid                                       bool
}

func readRecordedResources(ctx context.Context, data BaselineReader, vault *secretcrypto.Vault, d core.Deployment) recordedResources {
	var result recordedResources
	if d.ID == "" || d.AppID == "" || d.State != core.DeploymentSucceeded {
		return result
	}
	b, err := data.GetDriftBaseline(ctx, d.ID)
	if err != nil || b.DeploymentID != d.ID || b.AppID != d.AppID || b.ServerID == "" || b.Namespace == "" || b.Release == "" || len(b.Ciphertext) > 24<<20 {
		return result
	}
	raw, err := vault.Decrypt(scope(d.ID), b.Ciphertext)
	if err != nil || len(raw) > 16<<20 {
		return result
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var objects []map[string]any
	if decoder.Decode(&objects) != nil || len(objects) == 0 || len(objects) > 500 {
		return result
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return result
	}
	// A map ignores resource document order; marshaling sorts all JSON keys.
	// Every field remains in the compared object, including UID and Secret data.
	resources := make(map[string]map[string]any, len(objects))
	for _, obj := range objects {
		apiVersion, _ := obj["apiVersion"].(string)
		kind, _ := obj["kind"].(string)
		metadata, _ := obj["metadata"].(map[string]any)
		name, _ := metadata["name"].(string)
		namespace, _ := metadata["namespace"].(string)
		uid, _ := metadata["uid"].(string)
		if apiVersion == "" || kind == "" || kind == "List" || name == "" || namespace == "" || uid == "" || !safeRecordedNumbers(obj) {
			return result
		}
		identity, _ := json.Marshal([]string{apiVersion, kind, namespace, name})
		key := string(identity)
		if _, duplicate := resources[key]; duplicate {
			return result
		}
		resources[key] = obj
	}
	canonical, err := json.Marshal(resources)
	if err != nil {
		return result
	}
	return recordedResources{deployment: d.ID, app: d.AppID, server: b.ServerID, namespace: b.Namespace, release: b.Release, digest: sha256.Sum256(canonical), valid: true}
}

func safeRecordedNumbers(value any) bool {
	switch v := value.(type) {
	case json.Number:
		n, err := strconv.ParseFloat(string(v), 64)
		// Legacy baseline parsing used float64. At this boundary distinct large
		// integers may already have rounded to the same saved value.
		return err == nil && !math.IsInf(n, 0) && math.Abs(n) < 1<<53
	case map[string]any:
		for _, item := range v {
			if !safeRecordedNumbers(item) {
				return false
			}
		}
	case []any:
		for _, item := range v {
			if !safeRecordedNumbers(item) {
				return false
			}
		}
	}
	return true
}
