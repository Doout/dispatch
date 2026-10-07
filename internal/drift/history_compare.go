package drift

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/doout/dispatch/internal/core"
	secretcrypto "github.com/doout/dispatch/internal/crypto"
)

// RecordedComparison contains display-safe differences between saved manifests.
// Private values are compared before redaction, so a credential change is visible
// without returning its contents.
type RecordedComparison struct {
	Available bool
	Changes   []RecordedChange
	Hidden    int
	Truncated bool
	Message   string
}

type RecordedChange struct {
	Path, Kind    string
	Before, After any
}

const recordedRedacted = "[redacted]"

// CompareRecordedResources reads only encrypted deployment evidence. It neither
// reconstructs old charts nor reads current resources from a deployment target.
func CompareRecordedResources(ctx context.Context, data BaselineReader, vault *secretcrypto.Vault, from, to core.Deployment) RecordedComparison {
	result := RecordedComparison{Changes: []RecordedChange{}, Message: "Saved resources are unavailable for one of these deployments. Choose Saved inputs to compare the supplied values."}
	if data == nil || vault == nil || ctx.Err() != nil {
		return result
	}
	left, ok := comparisonResources(ctx, data, vault, from)
	if !ok {
		return result
	}
	right, ok := comparisonResources(ctx, data, vault, to)
	if !ok || ctx.Err() != nil {
		return result
	}
	result.Available = true
	result.Message = "Compared saved rendered resources. Hooks and live cluster changes are not included. Sensitive values are redacted."
	for _, location := range []struct{ path, before, after string }{
		{"/release/target", from.Snapshot.TargetID, to.Snapshot.TargetID},
		{"/release/namespace", from.Snapshot.Namespace, to.Snapshot.Namespace},
		{"/release/name", from.Snapshot.Release, to.Snapshot.Release},
	} {
		if location.before != location.after {
			result.add(location.path, location.before, true, location.after, true, false)
		}
	}
	for _, key := range recordedKeys(left, right) {
		before, bok := left[key]
		after, aok := right[key]
		if !bok || !aok {
			var b, a any
			if bok {
				b = "Present"
			}
			if aok {
				a = "Present"
			}
			result.add(key, b, bok, a, aok, false)
			continue
		}
		apiVersion, _ := before["apiVersion"].(string)
		kind, _ := before["kind"].(string)
		if !recordedStandardResource(apiVersion, kind) {
			// Custom resources can put credentials in arbitrary keys and values.
			result.compare(key+"/configuration", "", before, true, after, true, "", "", true)
			continue
		}
		result.compare(key, "", before, true, after, true, apiVersion, kind, false)
	}
	return result
}

func comparisonResources(ctx context.Context, data BaselineReader, vault *secretcrypto.Vault, d core.Deployment) (map[string]map[string]any, bool) {
	if d.ID == "" || d.AppID == "" || d.State != core.DeploymentSucceeded || d.Snapshot.TargetID == "" || d.Snapshot.Namespace == "" || d.Snapshot.Release == "" || ctx.Err() != nil {
		return nil, false
	}
	b, err := data.GetDriftBaseline(ctx, d.ID)
	if err != nil || b.DeploymentID != d.ID || b.AppID != d.AppID || b.ServerID != d.Snapshot.TargetID || b.Namespace != d.Snapshot.Namespace || b.Release != d.Snapshot.Release || len(b.Ciphertext) > 24<<20 {
		return nil, false
	}
	raw, err := vault.Decrypt(scope(d.ID), b.Ciphertext)
	if err != nil || len(raw) > 16<<20 {
		return nil, false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var objects []map[string]any
	if decoder.Decode(&objects) != nil || objects == nil || len(objects) > 500 {
		return nil, false
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return nil, false
	}
	resources := make(map[string]map[string]any, len(objects))
	nodes := 0
	for _, object := range objects {
		if ctx.Err() != nil || !validRecordedShape(object, 0, &nodes) || !safeRecordedNumbers(object) {
			return nil, false
		}
		apiVersion, aok := object["apiVersion"].(string)
		kind, kok := object["kind"].(string)
		metadata, mok := object["metadata"].(map[string]any)
		name, nok := metadata["name"].(string)
		namespace, nsok := metadata["namespace"].(string)
		if !aok || !kok || !mok || !nok || apiVersion == "" || kind == "" || kind == "List" || name == "" || metadata["namespace"] != nil && !nsok {
			return nil, false
		}
		for _, field := range []string{"labels", "annotations"} {
			if raw, present := metadata[field]; present {
				fields, ok := raw.(map[string]any)
				if !ok {
					return nil, false
				}
				for _, value := range fields {
					if _, ok := value.(string); !ok {
						return nil, false
					}
				}
			}
		}
		annotations, _ := metadata["annotations"].(map[string]any)
		if hook, _ := annotations["helm.sh/hook"].(string); hook != "" {
			return nil, false
		}
		key := "/resources/" + recordedPointer(apiVersion) + "/" + recordedPointer(kind) + "/" + recordedPointer(namespace) + "/" + recordedPointer(name)
		if _, duplicate := resources[key]; duplicate {
			return nil, false
		}
		normalizeRecordedMetadata(object)
		resources[key] = object
	}
	return resources, true
}

func validRecordedShape(value any, depth int, nodes *int) bool {
	(*nodes)++
	if depth > 64 || *nodes > 100000 {
		return false
	}
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			if len(key) > 1024 || !validRecordedShape(child, depth+1, nodes) {
				return false
			}
		}
	case []any:
		for _, child := range v {
			if !validRecordedShape(child, depth+1, nodes) {
				return false
			}
		}
	}
	return true
}

func normalizeRecordedMetadata(object map[string]any) {
	delete(object, "status")
	metadata, _ := object["metadata"].(map[string]any)
	for _, key := range []string{"uid", "resourceVersion", "generation", "creationTimestamp", "deletionTimestamp", "deletionGracePeriodSeconds", "managedFields", "selfLink"} {
		delete(metadata, key)
	}
	labels, _ := metadata["labels"].(map[string]any)
	for _, key := range []string{"dispatch.app/managed-by", "dispatch.app/project-id", "dispatch.app/app-id", "dispatch.app/deployment-id", "dispatch.app/workflow-resource-id", "dispatch.app/has-pr", "dispatch.app/pr-number", "dispatch.app", "dispatch.deployment", "dispatch.service-binding", "dispatch.release"} {
		delete(labels, key)
	}
	annotations, _ := metadata["annotations"].(map[string]any)
	for _, key := range []string{"dispatch.app/provenance", "kubectl.kubernetes.io/last-applied-configuration", "deployment.kubernetes.io/revision"} {
		delete(annotations, key)
	}
	for _, key := range []string{"labels", "annotations"} {
		if fields, ok := metadata[key].(map[string]any); ok && len(fields) == 0 {
			delete(metadata, key)
		}
	}
}

func (r *RecordedComparison) compare(path, field string, before any, bok bool, after any, aok bool, apiVersion, kind string, private bool) {
	if bok == aok && reflect.DeepEqual(before, after) {
		return
	}
	if private || recordedPrivateSection(field) {
		r.add(path, before, bok, after, aok, true)
		return
	}
	bm, bmap := before.(map[string]any)
	am, amap := after.(map[string]any)
	if (bmap || !bok) && (amap || !aok) && (bmap || amap) {
		keys := recordedKeys(bm, am)
		if len(keys) == 0 && bok != aok {
			r.add(path, before, bok, after, aok, true)
		}
		privateBefore, privateAfter := map[string]any{}, map[string]any{}
		for _, key := range keys {
			b, bp := bm[key]
			a, ap := am[key]
			if !recordedFieldName(key) {
				if bp {
					privateBefore[key] = b
				}
				if ap {
					privateAfter[key] = a
				}
				continue
			}
			r.compare(path+"/"+recordedPointer(key), field+"/"+key, b, bp, a, ap, apiVersion, kind, false)
		}
		r.compare(path+"/configuration", "", privateBefore, len(privateBefore) > 0, privateAfter, len(privateAfter) > 0, apiVersion, kind, true)
		return
	}
	bs, blist := before.([]any)
	as, alist := after.([]any)
	if (blist || !bok) && (alist || !aok) && (blist || alist) {
		if len(bs) == 0 && len(as) == 0 && bok != aok {
			r.add(path, before, bok, after, aok, true)
		}
		for i := 0; i < max(len(bs), len(as)); i++ {
			var b, a any
			if i < len(bs) {
				b = bs[i]
			}
			if i < len(as) {
				a = as[i]
			}
			part := "/" + strconv.Itoa(i)
			r.compare(path+part, field+part, b, i < len(bs), a, i < len(as), apiVersion, kind, false)
		}
		return
	}
	visible := (!bok || recordedVisibleScalar(apiVersion, kind, field, before)) && (!aok || recordedVisibleScalar(apiVersion, kind, field, after))
	r.add(path, before, bok, after, aok, !visible)
}

func (r *RecordedComparison) add(path string, before any, bok bool, after any, aok bool, redacted bool) {
	if len(r.Changes) == 1000 {
		r.Truncated = true
		return
	}
	kind := "changed"
	if !bok {
		kind = "added"
		before = nil
	}
	if !aok {
		kind = "removed"
		after = nil
	}
	if redacted {
		r.Hidden++
		if bok {
			before = recordedRedacted
		}
		if aok {
			after = recordedRedacted
		}
	}
	r.Changes = append(r.Changes, RecordedChange{path, kind, before, after})
}

// Traverse only familiar field names. Everything else is compared as one
// private section, since even a map key can contain a supplied credential.
func recordedFieldName(key string) bool {
	switch key {
	case "apiVersion", "kind", "metadata", "spec", "name", "namespace", "labels", "annotations", "template", "jobTemplate",
		"containers", "initContainers", "ephemeralContainers", "image", "imagePullPolicy", "env", "envFrom",
		"resources", "requests", "limits", "cpu", "memory", "ephemeral-storage", "replicas", "revisionHistoryLimit",
		"progressDeadlineSeconds", "minReadySeconds", "parallelism", "completions", "backoffLimit", "activeDeadlineSeconds",
		"ttlSecondsAfterFinished", "successfulJobsHistoryLimit", "failedJobsHistoryLimit", "startingDeadlineSeconds",
		"paused", "suspend", "terminationGracePeriodSeconds", "data", "stringData", "binaryData":
		return true
	}
	return false
}

func recordedPrivateSection(path string) bool {
	for _, part := range strings.Split(strings.ToLower(path), "/") {
		switch part {
		case "labels", "annotations", "data", "stringdata", "binarydata", "env", "envfrom", "command", "args", "ownerreferences", "finalizers", "selector", "nodeselector", "matchlabels", "matchexpressions":
			return true
		}
		for _, sensitive := range []string{"password", "passwd", "secret", "token", "apikey", "api_key", "privatekey", "private_key", "credential"} {
			if strings.Contains(part, sensitive) {
				return true
			}
		}
	}
	return false
}

func recordedStandardResource(apiVersion, kind string) bool {
	switch apiVersion {
	case "v1":
		return kind == "Pod" || kind == "ReplicationController" || kind == "Secret" || kind == "ConfigMap" || kind == "Service"
	case "apps/v1":
		return kind == "Deployment" || kind == "StatefulSet" || kind == "DaemonSet" || kind == "ReplicaSet"
	case "batch/v1":
		return kind == "Job" || kind == "CronJob"
	}
	return false
}

var recordedContainerField = regexp.MustCompile(`^/(?:initContainers|containers|ephemeralContainers)/[0-9]+/(.+)$`)
var recordedImage = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:/-]*(?:@sha256:[a-fA-F0-9]{64})?$`)
var recordedName = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*$`)
var recordedQuantity = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+|[numkKMGTP]i?)?$`)

func recordedVisibleScalar(apiVersion, kind, path string, value any) bool {
	workload := apiVersion == "apps/v1" || apiVersion == "batch/v1" || apiVersion == "v1" && (kind == "Pod" || kind == "ReplicationController")
	if !workload {
		return false
	}
	root := "/spec/"
	if kind == "CronJob" && strings.HasPrefix(path, "/spec/jobTemplate/spec/") {
		root = "/spec/jobTemplate/spec/"
	}
	if strings.HasPrefix(path, root) {
		field := strings.TrimPrefix(path, root)
		switch field {
		case "replicas", "revisionHistoryLimit", "progressDeadlineSeconds", "minReadySeconds", "parallelism", "completions", "backoffLimit", "activeDeadlineSeconds", "ttlSecondsAfterFinished", "successfulJobsHistoryLimit", "failedJobsHistoryLimit", "startingDeadlineSeconds":
			_, ok := value.(json.Number)
			return ok
		case "paused", "suspend":
			_, ok := value.(bool)
			return ok
		}
	}
	pod := "/spec/template/spec"
	if kind == "Pod" {
		pod = "/spec"
	} else if kind == "CronJob" {
		pod = "/spec/jobTemplate/spec/template/spec"
	}
	if !strings.HasPrefix(path, pod+"/") {
		return false
	}
	field := strings.TrimPrefix(path, pod)
	if field == "/terminationGracePeriodSeconds" || field == "/activeDeadlineSeconds" {
		_, ok := value.(json.Number)
		return ok
	}
	match := recordedContainerField.FindStringSubmatch(field)
	if len(match) == 0 {
		return false
	}
	text, stringValue := value.(string)
	switch match[1] {
	case "image":
		return stringValue && len(text) <= 1024 && !strings.Contains(text, "://") && recordedImage.MatchString(text)
	case "name":
		return stringValue && recordedName.MatchString(text)
	case "imagePullPolicy":
		return text == "Always" || text == "IfNotPresent" || text == "Never"
	case "resources/requests/cpu", "resources/limits/cpu", "resources/requests/memory", "resources/limits/memory", "resources/requests/ephemeral-storage", "resources/limits/ephemeral-storage":
		_, number := value.(json.Number)
		return number || stringValue && recordedQuantity.MatchString(text)
	}
	return false
}

func recordedPointer(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "~", "~0"), "/", "~1")
}

func recordedKeys[T any](left, right map[string]T) []string {
	keys := make(map[string]struct{}, len(left)+len(right))
	for key := range left {
		keys[key] = struct{}{}
	}
	for key := range right {
		keys[key] = struct{}{}
	}
	out := make([]string, 0, len(keys))
	for key := range keys {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}
