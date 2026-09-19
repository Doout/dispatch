package drift

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/doout/dispatch/internal/core"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"reflect"
	"sort"
	"strings"
)

// Merge compares only fields present in the deployed manifest. Admission defaults,
// injected sidecars and controller-owned fields remain untouched.
func Merge(want, live any, path string, diffs *[]core.DriftDifference) any {
	if object, ok := want.(map[string]any); ok {
		current, _ := live.(map[string]any)
		out := map[string]any{}
		for k, v := range current {
			out[k] = v
		}
		keys := make([]string, 0, len(object))
		for k := range object {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			out[k] = Merge(object[k], current[k], path+"/"+strings.ReplaceAll(strings.ReplaceAll(k, "~", "~0"), "/", "~1"), diffs)
		}
		return out
	}
	if list, ok := want.([]any); ok {
		current, _ := live.([]any)
		key := listKey(list)
		if key != "" {
			out := append([]any{}, current...)
			for _, v := range list {
				m := v.(map[string]any)
				index := -1
				for i, x := range out {
					lm, ok := x.(map[string]any)
					if ok && fmt.Sprint(lm[key]) == fmt.Sprint(m[key]) {
						index = i
						break
					}
				}
				p := path + "/" + fmt.Sprint(m[key])
				if index < 0 {
					out = append(out, Merge(v, nil, p, diffs))
				} else {
					out[index] = Merge(v, out[index], p, diffs)
				}
			}
			return out
		}
		objects := len(list) > 0
		for _, v := range list {
			if _, ok := v.(map[string]any); !ok {
				objects = false
				break
			}
		}
		if objects {
			out := make([]any, len(list))
			for i, v := range list {
				var actual any
				if i < len(current) {
					actual = current[i]
				}
				out[i] = Merge(v, actual, fmt.Sprintf("%s/%d", path, i), diffs)
			}
			return out
		}
		if len(list) == 0 && live == nil {
			return live
		}

	}
	if live == nil && (want == false || want == "" || want == int64(0) || want == float64(0)) {
		return live
	}
	if strings.Contains(path, "/resources/limits/") || strings.Contains(path, "/resources/requests/") {
		a, errA := resource.ParseQuantity(fmt.Sprint(want))
		b, errB := resource.ParseQuantity(fmt.Sprint(live))
		if errA == nil && errB == nil && a.Cmp(b) == 0 {
			return live
		}
	}
	if !reflect.DeepEqual(want, live) {
		a, _ := json.Marshal(want)
		b, _ := json.Marshal(live)
		if !bytes.Equal(a, b) {
			*diffs = append(*diffs, visibleDifference(path, want, live))
		}
	}
	return want
}
func visibleDifference(path string, want, live any) core.DriftDifference {
	d := core.DriftDifference{Path: path, Expected: want, Actual: live}
	sensitive := false
	for _, part := range []string{"password", "passwd", "secret", "token", "apikey", "api_key", "privatekey", "private_key", "credential"} {
		if strings.Contains(strings.ToLower(path), part) {
			sensitive = true
		}
	}
	safe := !sensitive && strings.HasPrefix(path, "/spec/") && (strings.Contains(path, "/containers/") || strings.Contains(path, "/initContainers/")) && strings.HasSuffix(path, "/image") && !strings.Contains(fmt.Sprint(want), "://") && !strings.Contains(fmt.Sprint(live), "://")
	if !safe {
		_, a := want.(string)
		_, b := live.(string)
		_, c := want.(map[string]any)
		_, e := want.([]any)
		_, f := live.(map[string]any)
		_, g := live.([]any)
		if sensitive || a || b || c || e || f || g {
			d.Expected, d.Actual, d.Redacted = "[redacted]", "[redacted]", true
		}
	}
	return d
}
func clean(obj *unstructured.Unstructured) *unstructured.Unstructured {
	obj = obj.DeepCopy()
	delete(obj.Object, "status")
	m, _, _ := unstructured.NestedMap(obj.Object, "metadata")
	for _, k := range []string{"uid", "resourceVersion", "generation", "creationTimestamp", "deletionTimestamp", "managedFields", "selfLink"} {
		delete(m, k)
	}
	if a, ok := m["annotations"].(map[string]any); ok {
		delete(a, "kubectl.kubernetes.io/last-applied-configuration")
		delete(a, "deployment.kubernetes.io/revision")
	}
	obj.Object["metadata"] = m
	return obj
}
func health(o *unstructured.Unstructured) string {
	if o.GetDeletionTimestamp() != nil {
		return "progressing"
	}
	observed, _, _ := unstructured.NestedInt64(o.Object, "status", "observedGeneration")
	count := func(k string) int64 { v, _, _ := unstructured.NestedInt64(o.Object, "status", k); return v }
	desired := int64(1)
	if n, ok, _ := unstructured.NestedInt64(o.Object, "spec", "replicas"); ok {
		desired = n
	}
	switch o.GetKind() {
	case "Deployment", "StatefulSet", "DaemonSet":
		conditions, _, _ := unstructured.NestedSlice(o.Object, "status", "conditions")
		for _, v := range conditions {
			c, _ := v.(map[string]any)
			if c["status"] == "True" && c["type"] == "ReplicaFailure" || c["status"] == "False" && c["reason"] == "ProgressDeadlineExceeded" {
				return "degraded"
			}
		}
		if observed < o.GetGeneration() {
			return "progressing"
		}
		if o.GetKind() == "DaemonSet" {
			if count("numberReady") == count("desiredNumberScheduled") && count("updatedNumberScheduled") == count("desiredNumberScheduled") {
				return "healthy"
			}
			return "progressing"
		}
		if count("readyReplicas") >= desired && count("updatedReplicas") >= desired {
			return "healthy"
		}
		return "progressing"
	case "Pod":
		phase, _, _ := unstructured.NestedString(o.Object, "status", "phase")
		if phase == "Failed" {
			return "degraded"
		}
		if phase == "Succeeded" {
			return "healthy"
		}
		conditions, _, _ := unstructured.NestedSlice(o.Object, "status", "conditions")
		for _, v := range conditions {
			c, _ := v.(map[string]any)
			if c["type"] == "Ready" && c["status"] == "True" {
				return "healthy"
			}
		}
		return "progressing"
	case "Job":
		conditions, _, _ := unstructured.NestedSlice(o.Object, "status", "conditions")
		for _, v := range conditions {
			c, _ := v.(map[string]any)
			if c["status"] == "True" {
				if c["type"] == "Failed" {
					return "degraded"
				}
				if c["type"] == "Complete" {
					return "healthy"
				}
			}
		}
		return "progressing"
	case "PersistentVolumeClaim":
		phase, _, _ := unstructured.NestedString(o.Object, "status", "phase")
		if phase == "Bound" {
			return "healthy"
		}
		if phase == "Lost" {
			return "degraded"
		}
		return "progressing"
	case "Secret", "ConfigMap", "Service", "ServiceAccount", "Role", "RoleBinding", "ClusterRole", "ClusterRoleBinding", "Namespace", "Ingress", "Route", "NetworkPolicy", "PodDisruptionBudget", "HorizontalPodAutoscaler", "CronJob":
		return "not_applicable"
	}
	return "unknown"
}

func listKey(items []any) string {
	if len(items) == 0 {
		return ""
	}
	for _, key := range []string{"name", "containerPort", "port", "mountPath", "type"} {
		seen := map[string]bool{}
		valid := true
		for _, item := range items {
			m, ok := item.(map[string]any)
			if !ok {
				valid = false
				break
			}
			v, present := m[key]
			if !present || v == nil || fmt.Sprint(v) == "" || seen[fmt.Sprint(v)] {
				valid = false
				break
			}
			seen[fmt.Sprint(v)] = true
		}
		if valid {
			return key
		}
	}
	return ""
}
