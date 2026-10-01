package deploy

import (
	"errors"
	"io"
	"strings"

	"gopkg.in/yaml.v3"
)

// Helm retains data by default. Explicit data deletion is a separate reviewed
// storage operation, never an incidental side effect of uninstall or rollback.
func protectHelmStorage(document map[string]any, labels map[string]string) {
	metadata, _ := document["metadata"].(map[string]any)
	if metadata == nil {
		metadata = map[string]any{}
		document["metadata"] = metadata
	}
	if document["kind"] == "PersistentVolumeClaim" || document["kind"] == "PersistentVolume" {
		ownerLabels, _ := metadata["labels"].(map[string]any)
		if ownerLabels == nil {
			ownerLabels = map[string]any{}
			metadata["labels"] = ownerLabels
		}
		for key, value := range labels {
			ownerLabels[key] = value
		}
		annotations, _ := metadata["annotations"].(map[string]any)
		if annotations == nil {
			annotations = map[string]any{}
			metadata["annotations"] = annotations
		}
		annotations["helm.sh/resource-policy"] = "keep"
	}
	if document["kind"] == "StatefulSet" {
		spec, _ := document["spec"].(map[string]any)
		if spec == nil {
			return
		}
		// Omitted retention already defaults to Retain, including on older clusters
		// that do not support an explicit retention-policy field.
		if spec["persistentVolumeClaimRetentionPolicy"] != nil {
			spec["persistentVolumeClaimRetentionPolicy"] = map[string]any{"whenDeleted": "Retain", "whenScaled": "Retain"}
		}
		templates, _ := spec["volumeClaimTemplates"].([]any)
		for _, value := range templates {
			claim, _ := value.(map[string]any)
			if claim == nil {
				continue
			}
			m, _ := claim["metadata"].(map[string]any)
			if m == nil {
				m = map[string]any{}
				claim["metadata"] = m
			}
			l, _ := m["labels"].(map[string]any)
			if l == nil {
				l = map[string]any{}
				m["labels"] = l
			}
			for k, v := range labels {
				l[k] = v
			}
		}
	}
	if document["kind"] == "List" {
		items, _ := document["items"].([]any)
		for _, value := range items {
			item, _ := value.(map[string]any)
			if item != nil {
				protectHelmStorage(item, labels)
			}
		}
	}
}

func checkHelmStorageCleanup(manifest string) error {
	decoder := yaml.NewDecoder(strings.NewReader(manifest))
	for {
		var item map[string]any
		err := decoder.Decode(&item)
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return errors.New("cannot inspect retained Helm storage")
		}
		if err := checkHelmStorageObject(item); err != nil {
			return err
		}
	}
}

func checkHelmStorageObject(item map[string]any) error {
	metadata, _ := item["metadata"].(map[string]any)
	annotations, _ := metadata["annotations"].(map[string]any)
	switch item["kind"] {
	case "Namespace":
		if annotations["helm.sh/resource-policy"] != "keep" {
			return errors.New("cleanup would delete a namespace and potentially protected storage; retain the namespace in the chart before cleanup")
		}
	case "PersistentVolumeClaim", "PersistentVolume":
		if annotations["helm.sh/resource-policy"] != "keep" {
			return errors.New("cleanup would delete protected Helm storage; upgrade the release with retained storage before cleanup")
		}
	case "StatefulSet":
		spec, _ := item["spec"].(map[string]any)
		policy, _ := spec["persistentVolumeClaimRetentionPolicy"].(map[string]any)
		if policy["whenDeleted"] == "Delete" || policy["whenScaled"] == "Delete" {
			return errors.New("cleanup would delete StatefulSet data; change PVC retention to Retain before cleanup")
		}
	case "List":
		items, _ := item["items"].([]any)
		for _, value := range items {
			child, _ := value.(map[string]any)
			if err := checkHelmStorageObject(child); err != nil {
				return err
			}
		}
	}
	return nil
}
