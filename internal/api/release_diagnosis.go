package api

import (
	"context"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/serviceconn"
	"github.com/go-chi/chi/v5"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
)

type diagnosisIssue struct {
	Resource  string                    `json:"resource"`
	Container string                    `json:"container,omitempty"`
	Reason    string                    `json:"reason"`
	Restarts  int32                     `json:"restarts,omitempty"`
	NextStep  string                    `json:"nextStep"`
	Logs      []deploymentResourceLog   `json:"logs"`
	Events    []deploymentResourceEvent `json:"events"`
}
type releaseDiagnosis struct {
	Location       string               `json:"location"`
	CheckedAt      time.Time            `json:"checkedAt"`
	Live           bool                 `json:"live"`
	Message        string               `json:"message"`
	Issues         []diagnosisIssue     `json:"issues"`
	DeploymentLogs []core.DeploymentLog `json:"deploymentLogs"`
}

func diagnosisNextStep(reason string) string {
	switch reason {
	case "ImagePullBackOff", "ErrImagePull", "InvalidImageName":
		return "Verify the image name and tag, registry access, and image pull Secret on this target."
	case "CrashLoopBackOff", "Error":
		return "Read the container's recent logs and exit code. Check startup configuration and dependency access."
	case "OOMKilled":
		return "Compare the container's memory limit with its usage. Fix excess allocation or increase the intended memory limit."
	case "FailedScheduling", "Unschedulable":
		return "Review node capacity, resource requests, affinity, taints, and unbound persistent volumes."
	case "CreateContainerConfigError", "CreateContainerError":
		return "Check referenced Secrets, ConfigMaps, volume mounts, and container security settings."
	case "Pending":
		return "Review scheduling events and persistent volume claims before retrying."
	case "ReadinessFailed", "Unhealthy":
		return "Check readiness probes, startup time, listening ports, and required service connections."
	default:
		return "Inspect this resource's events and logs, then correct the saved configuration before redeploying."
	}
}

func podDiagnosis(pod corev1.Pod) []diagnosisIssue {
	issues := []diagnosisIssue{}
	statuses := append(append([]corev1.ContainerStatus{}, pod.Status.InitContainerStatuses...), pod.Status.ContainerStatuses...)
	for _, status := range statuses {
		reason := ""
		if status.State.Waiting != nil {
			reason = status.State.Waiting.Reason
		}
		if status.State.Terminated != nil && status.State.Terminated.ExitCode != 0 {
			reason = status.State.Terminated.Reason
			if reason == "" {
				reason = "Error"
			}
		}
		if status.LastTerminationState.Terminated != nil && status.LastTerminationState.Terminated.Reason == "OOMKilled" {
			reason = "OOMKilled"
		}
		if reason == "" && !status.Ready && pod.Status.Phase == corev1.PodRunning {
			reason = "ReadinessFailed"
		}
		if reason != "" {
			issues = append(issues, diagnosisIssue{Resource: "Pod/" + pod.Name, Container: status.Name, Reason: reason, Restarts: status.RestartCount, NextStep: diagnosisNextStep(reason), Logs: []deploymentResourceLog{}, Events: []deploymentResourceEvent{}})
		}
	}
	if len(issues) == 0 && pod.Status.Phase == corev1.PodPending {
		reason := "Pending"
		for _, condition := range pod.Status.Conditions {
			if condition.Type == corev1.PodScheduled && condition.Status == corev1.ConditionFalse {
				reason = "Unschedulable"
			}
		}
		issues = append(issues, diagnosisIssue{Resource: "Pod/" + pod.Name, Reason: reason, NextStep: diagnosisNextStep(reason), Logs: []deploymentResourceLog{}, Events: []deploymentResourceEvent{}})
	}
	return issues
}

var diagnosticCredentialURL = regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://)[^\s/@:]+:[^\s/@]+@`)
var diagnosticCredentialAssignment = regexp.MustCompile(`(?i)((?:password|passwd|token|secret|api[_-]?key|authorization)\s*[:=]\s*)(?:"[^"]*"|'[^']*'|[^\s,;]+)`)

func diagnosticRedactor(values []string) func(string) string {
	sort.Slice(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
	return func(text string) string {
		for _, value := range values {
			if value != "" {
				text = strings.ReplaceAll(text, value, "[redacted]")
			}
		}
		text = diagnosticCredentialURL.ReplaceAllString(text, "${1}[redacted]@")
		text = diagnosticCredentialAssignment.ReplaceAllString(text, "${1}[redacted]")
		if len(text) > 32768 {
			text = text[:32768] + "\n[truncated]"
		}
		return text
	}
}

func (a *API) diagnoseDeploymentRelease(w http.ResponseWriter, r *http.Request) {
	d, err := a.store.GetDeployment(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.internal(w, err)
		return
	}
	err = a.deploy.WithIdleApplication(r.Context(), d.AppID, func() error { a.observeDeploymentDiagnosis(w, r); return nil })
	if err != nil {
		problem(w, 409, "Diagnosis temporarily unavailable", "Wait for the active deployment or runtime operation to finish, then inspect the settled workload.")
	}
}

func (a *API) observeDeploymentDiagnosis(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	d, err := a.store.GetDeployment(ctx, chi.URLParam(r, "id"))
	if err != nil {
		a.internal(w, err)
		return
	}
	result := releaseDiagnosis{Location: "Dispatch controller", CheckedAt: time.Now().UTC(), Message: "Current workload observation. Logs and resources can differ from an older selected deployment.", Issues: []diagnosisIssue{}, DeploymentLogs: []core.DeploymentLog{}}
	values := []string{}
	canReadLogs := true
	// The visible pods may belong to either the latest attempt or running release.
	// Resolve both captured inputs under the application's runtime lock before exposing logs.
	selected := map[string]bool{d.ID: true}
	if latest, err := a.store.LatestSuccessfulDeployment(ctx, d.AppID); err == nil {
		selected[latest.ID] = true
	}
	if history, err := a.store.ListApplicationHistory(ctx, d.AppID, "", 1); err == nil && len(history) > 0 {
		selected[history[0].ID] = true
	} else if err != nil {
		canReadLogs = false
	}
	resolver := serviceconn.Resolver{Vault: a.eventConfig.Vault, Secrets: a.eventConfig.SecretResolver}
	for deploymentID := range selected {
		bindings, err := a.store.GetDeploymentServiceBindings(ctx, deploymentID)
		if err != nil {
			canReadLogs = false
			break
		}
		for _, binding := range bindings {
			if binding.Service.ProjectID != d.App.ProjectID {
				canReadLogs = false
				break
			}
			resolved, err := resolver.Resolve(ctx, binding.Service)
			if err != nil {
				canReadLogs = false
				break
			}
			for key, field := range binding.Service.Fields {
				if field.SecretRef != "" && field.CapturedSecretValue == "" {
					canReadLogs = false
				}
				if field.Sensitive || field.SecretRef != "" {
					values = append(values, resolved[key])
				}
			}
			if binding.Service.Type == "postgresql" {
				values = append(values, resolved["connectionUrl"])
			}
		}
	}
	redact := diagnosticRedactor(values)
	if logs, e := a.store.ListDeploymentLogs(ctx, d.ID, 0); e == nil && canReadLogs {
		if len(logs) > 100 {
			logs = logs[len(logs)-100:]
		}
		for _, entry := range logs {
			entry.Message = redact(entry.Message)
			result.DeploymentLogs = append(result.DeploymentLogs, entry)
		}
	}
	if !canReadLogs {
		result.Message += " Logs are hidden because captured credential values cannot be safely redacted."
	}
	if d.Server == nil || d.Server.Kubernetes == nil {
		result.Message += " Pod diagnosis is available for Kubernetes and OpenShift; deployment logs are shown for this runtime."
		writeJSON(w, 200, result)
		return
	}
	namespace, release := deploymentRuntimeNames(d)
	if d.Snapshot.TargetID != "" && d.Snapshot.TargetID != d.App.ServerID {
		result.Message = "The application target changed. Live diagnosis for this historical target is unavailable."
		writeJSON(w, 200, result)
		return
	}
	if d.Snapshot.Namespace != "" {
		namespace, release = d.Snapshot.Namespace, d.Snapshot.Release
	}
	config, cleanup, err := kubernetesRESTConfig(*d.Server)
	if err != nil {
		result.Message = "Target credentials are unavailable. Inspect deployment logs and target configuration."
		writeJSON(w, 200, result)
		return
	}
	defer cleanup()
	config.Timeout = 10 * time.Second
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		result.Message = "Cannot initialize workload inspection."
		writeJSON(w, 200, result)
		return
	}
	pods, err := client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{Limit: 1000})
	if err != nil {
		result.Message = "Pods could not be read. Check target connectivity and namespace read permission."
		writeJSON(w, 200, result)
		return
	}
	result.Live = true
	if pods.Continue != "" {
		result.Message += " Only the first 1000 pods were inspected."
	}
	for _, pod := range pods.Items {
		object, e := runtime.DefaultUnstructuredConverter.ToUnstructured(&pod)
		if e != nil {
			continue
		}
		if !belongsToRelease(&unstructured.Unstructured{Object: object}, release) {
			continue
		}
		issues := podDiagnosis(pod)
		if len(issues) == 0 {
			continue
		}
		for _, container := range append(append([]corev1.Container{}, pod.Spec.InitContainers...), pod.Spec.Containers...) {
			for _, env := range container.Env {
				if sensitivePath(env.Name) && env.Value != "" {
					values = append(values, env.Value)
				}
			}
		}
		redact = diagnosticRedactor(values)
		events, e := kubernetesObjectEvents(ctx, client, namespace, string(pod.UID))
		if e != nil {
			events = []deploymentResourceEvent{}
		}
		if len(events) > 10 {
			events = events[:10]
		}
		for index := range events {
			if canReadLogs {
				events[index].Message = redact(events[index].Message)
			} else {
				events[index].Message = "Event details hidden because captured credentials cannot be safely redacted."
			}
		}
		for index := range issues {
			issues[index].Events = events
			if canReadLogs && issues[index].Container != "" {
				tail, limit := int64(80), int64(16384)
				content, e := client.CoreV1().Pods(namespace).GetLogs(pod.Name, &corev1.PodLogOptions{Container: issues[index].Container, TailLines: &tail, LimitBytes: &limit, Previous: issues[index].Restarts > 0, Timestamps: true}).DoRaw(ctx)
				entry := deploymentResourceLog{Container: issues[index].Container}
				if e == nil {
					entry.Content = redact(string(content))
				} else {
					entry.Error = "Recent container logs are unavailable."
				}
				issues[index].Logs = []deploymentResourceLog{entry}
			}
		}
		result.Issues = append(result.Issues, issues...)
		if len(result.Issues) >= 20 {
			result.Message += " Showing the first 20 failing containers."
			result.Issues = result.Issues[:20]
			break
		}
	}
	writeJSON(w, 200, result)
}
