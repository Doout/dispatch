package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/deploy"
	"github.com/doout/dispatch/internal/kubeconfig"
	"github.com/doout/dispatch/internal/store"
	"github.com/go-chi/chi/v5"
	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

type topologyColumn struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}
type topologyNode struct {
	ID       string            `json:"id"`
	Column   string            `json:"column"`
	Kind     string            `json:"kind"`
	Label    string            `json:"label"`
	Detail   string            `json:"detail,omitempty"`
	State    string            `json:"state,omitempty"`
	Href     string            `json:"href,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}
type topologyEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
	Kind string `json:"kind"`
}
type runtimeTopology struct {
	Columns []topologyColumn `json:"columns"`
	Nodes   []topologyNode   `json:"nodes"`
	Edges   []topologyEdge   `json:"edges"`
}
type appliedValue struct {
	Path     string `json:"path"`
	Value    any    `json:"value"`
	Redacted bool   `json:"redacted,omitempty"`
}
type deploymentTopologyResponse struct {
	Topology  runtimeTopology `json:"topology"`
	Target    string          `json:"target"`
	Runtime   string          `json:"runtime"`
	Namespace string          `json:"namespace"`
	Release   string          `json:"release"`
	Chart     string          `json:"chart,omitempty"`
	Values    []appliedValue  `json:"values"`
	Live      bool            `json:"live"`
	Warning   string          `json:"warning,omitempty"`
}

type deploymentManifest struct {
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	APIVersion string `json:"apiVersion"`
	Document   string `json:"document"`
}

type deploymentManifestOrigin struct {
	Managed         bool   `json:"managed"`
	Repository      string `json:"repository,omitempty"`
	Branch          string `json:"branch,omitempty"`
	ConfigPath      string `json:"configPath,omitempty"`
	ConfigRevision  string `json:"configRevision,omitempty"`
	ChartRepository string `json:"chartRepository,omitempty"`
	ChartPath       string `json:"chartPath,omitempty"`
}

type deploymentManifestsResponse struct {
	Target    string                   `json:"target"`
	Namespace string                   `json:"namespace"`
	Release   string                   `json:"release"`
	Origin    deploymentManifestOrigin `json:"origin"`
	Manifests []deploymentManifest     `json:"manifests"`
	Warning   string                   `json:"warning,omitempty"`
}

type deploymentResourceLog struct {
	Container string `json:"container"`
	Content   string `json:"content,omitempty"`
	Error     string `json:"error,omitempty"`
}

type deploymentResourceEvent struct {
	Type     string `json:"type"`
	Reason   string `json:"reason"`
	Message  string `json:"message"`
	Count    int32  `json:"count"`
	LastSeen string `json:"lastSeen"`
}

type deploymentResourceResponse struct {
	Manifest deploymentManifest        `json:"manifest"`
	Loggable bool                      `json:"loggable"`
	Logs     []deploymentResourceLog   `json:"logs"`
	Events   []deploymentResourceEvent `json:"events"`
	Warning  string                    `json:"warning,omitempty"`
}

func (a *API) getDeploymentTopology(w http.ResponseWriter, r *http.Request) {
	item, err := a.store.GetDeployment(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) {
		problem(w, http.StatusNotFound, "Deployment not found", "The deployment record does not exist.")
		return
	}
	if err != nil {
		a.internal(w, err)
		return
	}
	result := deploymentTopologyResponse{Target: item.Server.Name, Runtime: string(item.Server.Runtime), Chart: item.App.HelmChart}
	result.Namespace = item.App.HelmNamespace
	if result.Namespace == "" && item.Server.Kubernetes != nil {
		result.Namespace = item.Server.Kubernetes.Namespace
	}
	if result.Namespace == "" {
		result.Namespace = "default"
	}
	result.Release = item.App.HelmRelease
	if result.Release == "" {
		result.Release = item.App.Name
	}
	result.Values = visibleHelmValues(item.App)
	if item.Snapshot.TargetID != "" {
		result.Target, result.Runtime, result.Namespace, result.Release, result.Chart = item.Snapshot.TargetName, item.Snapshot.Runtime, item.Snapshot.Namespace, item.Snapshot.Release, item.Snapshot.Chart
		result.Values = visibleValueMap(item.Snapshot.Values)
	}
	result.Topology = releaseRoot(item, result.Namespace, result.Release)
	if item.Server.Kubernetes != nil {
		if graph, inspectErr := inspectKubernetesRelease(r.Context(), *item.Server, result.Namespace, result.Release); inspectErr == nil {
			result.Topology, result.Live = graph, true
		} else {
			result.Warning = "Live cluster inventory is unavailable: " + inspectErr.Error()
		}
	} else {
		result.Warning = "Live topology is available for Kubernetes and OpenShift targets."
	}
	writeJSON(w, http.StatusOK, result)
}

func (a *API) getDeploymentManifests(w http.ResponseWriter, r *http.Request) {
	item, err := a.store.GetDeployment(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) {
		problem(w, http.StatusNotFound, "Deployment not found", "The deployment record does not exist.")
		return
	}
	if err != nil {
		a.internal(w, err)
		return
	}

	namespace, release := deploymentRuntimeNames(item)
	result := deploymentManifestsResponse{
		Target: item.Server.Name, Namespace: namespace, Release: release,
		Origin:    deploymentManifestOrigin{Repository: repositoryLabel(item.App.SourceRepo), Branch: item.App.Branch, ChartRepository: repositoryLabel(item.App.SourceRepo), ChartPath: item.App.HelmChart},
		Manifests: []deploymentManifest{},
	}
	if item.Snapshot.TargetID != "" {
		result.Target, result.Namespace, result.Release = item.Snapshot.TargetName, item.Snapshot.Namespace, item.Snapshot.Release
	}
	result.Origin = a.deploymentManifestOrigin(r.Context(), item, result.Origin)
	if item.Server.Kubernetes == nil {
		result.Warning = "Rendered manifests are available for Kubernetes and OpenShift targets."
		writeJSON(w, http.StatusOK, result)
		return
	}
	rendered, inspectErr := deploy.HelmReleaseManifest(r.Context(), *item.Server, result.Namespace, result.Release)
	if inspectErr == nil {
		result.Manifests = parseReleaseManifests(rendered)
	} else {
		objects, liveErr := listKubernetesReleaseObjects(r.Context(), *item.Server, result.Namespace, result.Release)
		if liveErr != nil {
			result.Warning = "Release manifests are unavailable: " + inspectErr.Error()
			writeJSON(w, http.StatusOK, result)
			return
		}
		result.Warning = "Showing live resources because the stored Helm manifest is unavailable."
		for _, object := range objects {
			result.Manifests = append(result.Manifests, encodeDeploymentManifest(object))
		}
	}
	writeJSON(w, http.StatusOK, result)
}

func (a *API) getDeploymentResource(w http.ResponseWriter, r *http.Request) {
	item, err := a.store.GetDeployment(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) {
		problem(w, http.StatusNotFound, "Deployment not found", "The deployment record does not exist.")
		return
	}
	if err != nil {
		a.internal(w, err)
		return
	}
	if item.Server.Kubernetes == nil {
		problem(w, http.StatusBadRequest, "Live resource unavailable", "Resource inspection requires a Kubernetes or OpenShift target.")
		return
	}

	kindName := strings.ToLower(strings.TrimSpace(chi.URLParam(r, "kind")))
	name := strings.TrimSpace(chi.URLParam(r, "name"))
	requestedKind, ok := inspectedResourceKind(kindName)
	if !ok || name == "" {
		problem(w, http.StatusBadRequest, "Invalid resource", "Choose a resource from the deployment topology.")
		return
	}
	namespace, release := deploymentRuntimeNames(item)
	objects, err := listKubernetesReleaseObjects(r.Context(), *item.Server, namespace, release)
	if err != nil {
		a.internal(w, err)
		return
	}
	var selected *unstructured.Unstructured
	for _, object := range objects {
		kind, supported := inspectedKind(object)
		if supported && kind.kind == requestedKind.kind && object.GetName() == name {
			selected = object
			break
		}
	}
	if selected == nil {
		problem(w, http.StatusNotFound, "Resource not found", "The resource is no longer part of this release.")
		return
	}

	result := deploymentResourceResponse{Manifest: encodeDeploymentManifest(selected), Loggable: kindName == "pod", Logs: []deploymentResourceLog{}, Events: []deploymentResourceEvent{}}
	config, cleanup, err := kubernetesRESTConfig(*item.Server)
	if err != nil {
		result.Warning = err.Error()
		writeJSON(w, http.StatusOK, result)
		return
	}
	defer cleanup()
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		result.Warning = err.Error()
		writeJSON(w, http.StatusOK, result)
		return
	}
	result.Events, err = kubernetesObjectEvents(r.Context(), client, namespace, string(selected.GetUID()))
	if err != nil {
		result.Warning = "Events are unavailable: " + err.Error()
	}
	if result.Loggable {
		result.Logs = kubernetesPodLogs(r.Context(), client, namespace, selected)
	}
	writeJSON(w, http.StatusOK, result)
}

func parseReleaseManifests(document string) []deploymentManifest {
	decoder := yaml.NewDecoder(strings.NewReader(document))
	items := []deploymentManifest{}
	for {
		var value map[string]any
		if err := decoder.Decode(&value); err != nil {
			break
		}
		if len(value) == 0 {
			continue
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			continue
		}
		var normalized map[string]any
		if json.Unmarshal(encoded, &normalized) != nil {
			continue
		}
		object := &unstructured.Unstructured{Object: normalized}
		if object.GetName() == "" || object.GetKind() == "" {
			continue
		}
		items = append(items, encodeDeploymentManifest(object))
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Kind == items[j].Kind {
			return items[i].Name < items[j].Name
		}
		return items[i].Kind < items[j].Kind
	})
	return items
}

func encodeDeploymentManifest(object *unstructured.Unstructured) deploymentManifest {
	document, _ := yaml.Marshal(manifestObject(object))
	return deploymentManifest{Name: object.GetName(), Kind: object.GetKind(), APIVersion: object.GetAPIVersion(), Document: string(document)}
}

func deploymentRuntimeNames(item core.Deployment) (string, string) {
	namespace := item.App.HelmNamespace
	if namespace == "" && item.Server.Kubernetes != nil {
		namespace = item.Server.Kubernetes.Namespace
	}
	if namespace == "" {
		namespace = "default"
	}
	release := item.App.HelmRelease
	if release == "" {
		release = item.App.Name
	}
	return namespace, release
}

func (a *API) deploymentManifestOrigin(ctx context.Context, item core.Deployment, fallback deploymentManifestOrigin) deploymentManifestOrigin {
	runs, err := a.store.ListWorkflowStageRuns(ctx, "")
	if err != nil {
		return fallback
	}
	for _, run := range runs {
		matched := false
		for _, deploymentID := range run.DeploymentIDs {
			if deploymentID == item.ID {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		revision, revisionErr := a.store.GetWorkflowRevision(ctx, run.RevisionID)
		if revisionErr != nil {
			return fallback
		}
		resource, resourceErr := a.store.GetWorkflowResource(ctx, revision.ResourceID)
		if resourceErr != nil {
			return fallback
		}
		source, sourceErr := a.store.GetConfigSource(ctx, resource.ConfigSourceID)
		if sourceErr != nil {
			return fallback
		}
		fallback.Managed = true
		fallback.Repository = repositoryLabel(source.Repository)
		fallback.Branch = source.Branch
		fallback.ConfigPath = resource.Path
		fallback.ConfigRevision = shortRevision(revision.ConfigSHA)
		return fallback
	}
	return fallback
}

func repositoryLabel(value string) string {
	trimmed := strings.TrimSpace(strings.TrimSuffix(value, ".git"))
	if parsed, err := url.Parse(trimmed); err == nil && parsed.Host != "" {
		return strings.TrimPrefix(parsed.Path, "/")
	}
	if colon := strings.LastIndex(trimmed, ":"); colon >= 0 {
		trimmed = trimmed[colon+1:]
	}
	return strings.TrimPrefix(trimmed, "/")
}

func (a *API) getServerTopology(w http.ResponseWriter, r *http.Request) {
	server, err := a.store.GetServer(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) {
		problem(w, http.StatusNotFound, "Deployment target not found", "The target does not exist.")
		return
	}
	if err != nil {
		a.internal(w, err)
		return
	}
	apps, err := a.store.ListApps(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	deployments, err := a.store.ListDeployments(r.Context(), 200)
	if err != nil {
		a.internal(w, err)
		return
	}
	latest := map[string]core.Deployment{}
	for _, deployment := range deployments {
		if _, ok := latest[deployment.AppID]; !ok {
			latest[deployment.AppID] = deployment
		}
	}
	knownApps := make(map[string]struct{}, len(apps))
	for _, app := range apps {
		knownApps[app.ID] = struct{}{}
	}
	for _, deployment := range latest {
		if deployment.App == nil || deployment.Server == nil || deployment.Server.ID != server.ID || deployment.App.Template {
			continue
		}
		if _, ok := knownApps[deployment.App.ID]; ok {
			continue
		}
		apps = append(apps, *deployment.App)
		knownApps[deployment.App.ID] = struct{}{}
	}
	sort.Slice(apps, func(i, j int) bool {
		if apps[i].Name == apps[j].Name {
			return apps[i].ID < apps[j].ID
		}
		return apps[i].Name < apps[j].Name
	})
	graph := runtimeTopology{Columns: []topologyColumn{{"target", "Target"}, {"namespace", "Namespaces"}, {"release", "Releases"}}}
	root := "target:" + server.ID
	graph.Nodes = append(graph.Nodes, topologyNode{ID: root, Column: "target", Kind: "target", Label: server.Name, Detail: string(server.Runtime), State: string(server.State)})
	namespaces := map[string]string{}
	for _, app := range apps {
		if app.ServerID != server.ID || app.Template {
			continue
		}
		namespace := app.HelmNamespace
		if namespace == "" && server.Kubernetes != nil {
			namespace = server.Kubernetes.Namespace
		}
		if namespace == "" {
			namespace = "default"
		}
		namespaceID := "namespace:" + namespace
		if _, ok := namespaces[namespace]; !ok {
			namespaces[namespace] = namespaceID
			graph.Nodes = append(graph.Nodes, topologyNode{ID: namespaceID, Column: "namespace", Kind: "namespace", Label: namespace})
			graph.Edges = append(graph.Edges, topologyEdge{root, namespaceID, "contains"})
		}
		release := app.HelmRelease
		if release == "" {
			release = app.Name
		}
		node := topologyNode{ID: "release:" + app.ID, Column: "release", Kind: "release", Label: release, Detail: app.Name, State: app.State, Metadata: map[string]string{"Chart": app.HelmChart}}
		if deployment, ok := latest[app.ID]; ok {
			node.State = string(deployment.State)
			node.Href = "/deployments/" + deployment.ID + "/topology"
			node.Metadata["Revision"] = shortRevision(deployment.CommitSHA)
			node.Metadata["Deployed"] = deployment.CreatedAt.Format("2006-01-02 15:04 UTC")
		}
		graph.Nodes = append(graph.Nodes, node)
		graph.Edges = append(graph.Edges, topologyEdge{namespaceID, node.ID, "deploys"})
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, graph)
}

func releaseRoot(item core.Deployment, namespace, release string) runtimeTopology {
	return runtimeTopology{Columns: []topologyColumn{{"release", "Release"}}, Nodes: []topologyNode{{ID: "release:" + release, Column: "release", Kind: "release", Label: release, Detail: namespace, State: string(item.State), Metadata: map[string]string{"Target": item.Server.Name, "Revision": shortRevision(item.CommitSHA)}}}}
}

type resourceKind struct {
	column, kind string
	gvr          schema.GroupVersionResource
}

var inspectedKinds = []resourceKind{
	{"workload", "deployment", schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}},
	{"workload", "statefulset", schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "statefulsets"}},
	{"workload", "daemonset", schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "daemonsets"}},
	{"workload", "hpa", schema.GroupVersionResource{Group: "autoscaling", Version: "v2", Resource: "horizontalpodautoscalers"}},
	{"pod", "pod", schema.GroupVersionResource{Version: "v1", Resource: "pods"}},
	{"access", "service", schema.GroupVersionResource{Version: "v1", Resource: "services"}},
	{"access", "ingress", schema.GroupVersionResource{Group: "networking.k8s.io", Version: "v1", Resource: "ingresses"}},
	{"access", "route", schema.GroupVersionResource{Group: "route.openshift.io", Version: "v1", Resource: "routes"}},
	{"access", "pvc", schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumeclaims"}},
}

func inspectedResourceKind(name string) (resourceKind, bool) {
	for _, kind := range inspectedKinds {
		if kind.kind == name {
			return kind, true
		}
	}
	return resourceKind{}, false
}

func inspectKubernetesRelease(ctx context.Context, server core.Server, namespace, release string) (runtimeTopology, error) {
	listed, err := listKubernetesReleaseObjects(ctx, server, namespace, release)
	if err != nil {
		return runtimeTopology{}, err
	}
	graph := runtimeTopology{Columns: []topologyColumn{{"release", "Release"}, {"workload", "Workloads"}, {"pod", "Pods"}, {"access", "Access and storage"}}}
	root := "release:" + release
	graph.Nodes = append(graph.Nodes, topologyNode{ID: root, Column: "release", Kind: "release", Label: release, Detail: namespace, State: "live", Metadata: map[string]string{"Target": server.Name}})
	objects := map[string]*unstructured.Unstructured{}
	for _, object := range listed {
		kind, ok := inspectedKind(object)
		if !ok {
			continue
		}
		id := objectID(kind.kind, object.GetName())
		objects[id] = object
		graph.Nodes = append(graph.Nodes, resourceNode(kind, object))
	}
	for id, object := range objects {
		kind := strings.SplitN(id, ":", 2)[0]
		connected := false
		if kind == "pod" {
			for otherID, workload := range objects {
				if isWorkload(otherID) && selectorMatches(workload, object.GetLabels()) {
					graph.Edges = append(graph.Edges, topologyEdge{otherID, id, "owns"})
					connected = true
				}
			}
		}
		if kind == "service" {
			selector, _, _ := unstructured.NestedStringMap(object.Object, "spec", "selector")
			for otherID, pod := range objects {
				if strings.HasPrefix(otherID, "pod:") && labelsMatch(selector, pod.GetLabels()) {
					graph.Edges = append(graph.Edges, topologyEdge{otherID, id, "serves"})
					connected = true
				}
			}
		}
		if kind == "hpa" {
			targetKind, _, _ := unstructured.NestedString(object.Object, "spec", "scaleTargetRef", "kind")
			targetName, _, _ := unstructured.NestedString(object.Object, "spec", "scaleTargetRef", "name")
			targetID := strings.ToLower(targetKind) + ":" + targetName
			if _, ok := objects[targetID]; ok {
				graph.Edges = append(graph.Edges, topologyEdge{id, targetID, "scales"})
				graph.Edges = append(graph.Edges, topologyEdge{root, id, "configures"})
				connected = true
			}
		}
		if kind == "route" {
			service, _, _ := unstructured.NestedString(object.Object, "spec", "to", "name")
			if _, ok := objects["service:"+service]; ok {
				graph.Edges = append(graph.Edges, topologyEdge{"service:" + service, id, "exposes"})
				connected = true
			}
		}
		if !connected && kind != "pod" && kind != "service" && kind != "route" {
			graph.Edges = append(graph.Edges, topologyEdge{root, id, "contains"})
		}
	}
	return graph, nil
}

func listKubernetesReleaseObjects(ctx context.Context, server core.Server, namespace, release string) ([]*unstructured.Unstructured, error) {
	config, cleanup, err := kubernetesRESTConfig(server)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	client, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	return listKubernetesReleaseObjectsWithClient(ctx, client, namespace, release), nil
}

func kubernetesRESTConfig(server core.Server) (*rest.Config, func(), error) {
	prepared, cleanup, err := kubeconfig.Prepare(*server.Kubernetes)
	if err != nil {
		return nil, func() {}, err
	}
	rules := &clientcmd.ClientConfigLoadingRules{ExplicitPath: prepared.KubeconfigPath}
	overrides := &clientcmd.ConfigOverrides{CurrentContext: prepared.Context}
	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides).ClientConfig()
	if err != nil {
		cleanup()
		return nil, func() {}, err
	}
	return config, cleanup, nil
}

func listKubernetesReleaseObjectsWithClient(ctx context.Context, client dynamic.Interface, namespace, release string) []*unstructured.Unstructured {
	objects := []*unstructured.Unstructured{}
	for _, kind := range inspectedKinds {
		list, listErr := client.Resource(kind.gvr).Namespace(namespace).List(ctx, metav1.ListOptions{})
		if listErr != nil {
			continue
		}
		for index := range list.Items {
			object := &list.Items[index]
			if !belongsToRelease(object, release) {
				continue
			}
			if object.GetKind() == "" {
				object.SetKind(manifestKind(kind.kind))
			}
			if object.GetAPIVersion() == "" {
				object.SetAPIVersion(kind.gvr.GroupVersion().String())
			}
			objects = append(objects, object.DeepCopy())
		}
	}
	sort.Slice(objects, func(i, j int) bool {
		if objects[i].GetKind() == objects[j].GetKind() {
			return objects[i].GetName() < objects[j].GetName()
		}
		return objects[i].GetKind() < objects[j].GetKind()
	})
	return objects
}

func kubernetesPodLogs(ctx context.Context, client kubernetes.Interface, namespace string, object *unstructured.Unstructured) []deploymentResourceLog {
	containers := []string{}
	seen := map[string]bool{}
	for _, field := range []string{"initContainers", "containers", "ephemeralContainers"} {
		items, _, _ := unstructured.NestedSlice(object.Object, "spec", field)
		for _, raw := range items {
			container, _ := raw.(map[string]any)
			name, _, _ := unstructured.NestedString(container, "name")
			if name != "" && !seen[name] {
				seen[name] = true
				containers = append(containers, name)
			}
		}
	}
	tailLines := int64(500)
	logs := make([]deploymentResourceLog, 0, len(containers))
	for _, container := range containers {
		content, err := client.CoreV1().Pods(namespace).GetLogs(object.GetName(), &corev1.PodLogOptions{Container: container, TailLines: &tailLines, Timestamps: true}).DoRaw(ctx)
		entry := deploymentResourceLog{Container: container, Content: string(content)}
		if err != nil {
			entry.Content = ""
			entry.Error = err.Error()
		}
		logs = append(logs, entry)
	}
	return logs
}

func kubernetesObjectEvents(ctx context.Context, client kubernetes.Interface, namespace, uid string) ([]deploymentResourceEvent, error) {
	items, err := client.CoreV1().Events(namespace).List(ctx, metav1.ListOptions{FieldSelector: "involvedObject.uid=" + uid})
	if err != nil {
		return nil, err
	}
	events := make([]deploymentResourceEvent, 0, len(items.Items))
	for _, item := range items.Items {
		events = append(events, deploymentResourceEvent{Type: item.Type, Reason: item.Reason, Message: item.Message, Count: item.Count, LastSeen: kubernetesEventTime(item).Format(time.RFC3339)})
	}
	sort.Slice(events, func(i, j int) bool { return events[i].LastSeen > events[j].LastSeen })
	return events, nil
}

func kubernetesEventTime(event corev1.Event) time.Time {
	if !event.LastTimestamp.IsZero() {
		return event.LastTimestamp.Time
	}
	if !event.EventTime.IsZero() {
		return event.EventTime.Time
	}
	if !event.FirstTimestamp.IsZero() {
		return event.FirstTimestamp.Time
	}
	return event.CreationTimestamp.Time
}

func inspectedKind(object *unstructured.Unstructured) (resourceKind, bool) {
	for _, kind := range inspectedKinds {
		if strings.EqualFold(manifestKind(kind.kind), object.GetKind()) {
			return kind, true
		}
	}
	return resourceKind{}, false
}

func manifestKind(kind string) string {
	switch kind {
	case "hpa":
		return "HorizontalPodAutoscaler"
	case "pvc":
		return "PersistentVolumeClaim"
	case "statefulset":
		return "StatefulSet"
	case "daemonset":
		return "DaemonSet"
	default:
		return strings.ToUpper(kind[:1]) + kind[1:]
	}
}

func manifestObject(object *unstructured.Unstructured) map[string]any {
	manifest := object.DeepCopy().Object
	delete(manifest, "status")
	metadata, _, _ := unstructured.NestedMap(manifest, "metadata")
	for _, field := range []string{"creationTimestamp", "generation", "managedFields", "resourceVersion", "selfLink", "uid"} {
		delete(metadata, field)
	}
	if len(metadata) > 0 {
		manifest["metadata"] = metadata
	}
	if strings.EqualFold(object.GetKind(), "Secret") {
		if data, ok := manifest["data"].(map[string]any); ok {
			for key := range data {
				data[key] = "REDACTED"
			}
		}
		delete(manifest, "stringData")
	}
	return manifest
}

func belongsToRelease(object *unstructured.Unstructured, release string) bool {
	return object.GetLabels()["app.kubernetes.io/instance"] == release || object.GetAnnotations()["meta.helm.sh/release-name"] == release || object.GetName() == release || strings.HasPrefix(object.GetName(), release+"-")
}
func objectID(kind, name string) string { return kind + ":" + name }
func isWorkload(id string) bool {
	return strings.HasPrefix(id, "deployment:") || strings.HasPrefix(id, "statefulset:") || strings.HasPrefix(id, "daemonset:")
}
func selectorMatches(workload *unstructured.Unstructured, labels map[string]string) bool {
	match, _, _ := unstructured.NestedStringMap(workload.Object, "spec", "selector", "matchLabels")
	return labelsMatch(match, labels)
}
func labelsMatch(selector, labels map[string]string) bool {
	if len(selector) == 0 {
		return false
	}
	for key, value := range selector {
		if labels[key] != value {
			return false
		}
	}
	return true
}

func resourceNode(kind resourceKind, object *unstructured.Unstructured) topologyNode {
	node := topologyNode{ID: objectID(kind.kind, object.GetName()), Column: kind.column, Kind: kind.kind, Label: object.GetName(), Metadata: map[string]string{}}
	switch kind.kind {
	case "deployment", "statefulset":
		replicas, _, _ := unstructured.NestedInt64(object.Object, "status", "replicas")
		ready, _, _ := unstructured.NestedInt64(object.Object, "status", "readyReplicas")
		node.State = fmt.Sprintf("%d/%d ready", ready, replicas)
	case "daemonset":
		replicas, _, _ := unstructured.NestedInt64(object.Object, "status", "desiredNumberScheduled")
		ready, _, _ := unstructured.NestedInt64(object.Object, "status", "numberReady")
		node.State = fmt.Sprintf("%d/%d ready", ready, replicas)
	case "pod":
		node.State, _, _ = unstructured.NestedString(object.Object, "status", "phase")
		statuses, _, _ := unstructured.NestedSlice(object.Object, "status", "containerStatuses")
		restarts := int64(0)
		ready := 0
		for _, raw := range statuses {
			status, _ := raw.(map[string]any)
			value, _, _ := unstructured.NestedInt64(status, "restartCount")
			restarts += value
			ok, _, _ := unstructured.NestedBool(status, "ready")
			if ok {
				ready++
			}
		}
		node.Metadata["Containers"] = fmt.Sprintf("%d/%d ready", ready, len(statuses))
		node.Metadata["Restarts"] = strconv.FormatInt(restarts, 10)
	case "hpa":
		current, _, _ := unstructured.NestedInt64(object.Object, "status", "currentReplicas")
		min, _, _ := unstructured.NestedInt64(object.Object, "spec", "minReplicas")
		max, _, _ := unstructured.NestedInt64(object.Object, "spec", "maxReplicas")
		node.State = fmt.Sprintf("%d replicas", current)
		node.Metadata["Range"] = fmt.Sprintf("%d–%d", min, max)
	case "service":
		node.State, _, _ = unstructured.NestedString(object.Object, "spec", "type")
		ip, _, _ := unstructured.NestedString(object.Object, "spec", "clusterIP")
		node.Metadata["Cluster IP"] = ip
	case "route":
		host, _, _ := unstructured.NestedString(object.Object, "spec", "host")
		node.State = "exposed"
		node.Metadata["Host"] = host
	case "ingress":
		node.State = "exposed"
	case "pvc":
		node.State, _, _ = unstructured.NestedString(object.Object, "status", "phase")
	}
	if node.State == "" {
		node.State = "configured"
	}
	return node
}

func visibleHelmValues(app *core.App) []appliedValue {
	if app == nil {
		return []appliedValue{}
	}
	merged := map[string]any{}
	for _, document := range []string{app.HelmValues, app.HelmGeneratedValues, app.HelmGroupValues} {
		if strings.TrimSpace(document) == "" {
			continue
		}
		var values map[string]any
		if yaml.Unmarshal([]byte(document), &values) == nil {
			mergeValues(merged, values)
		}
	}
	items := []appliedValue{}
	flattenValues("", merged, &items)
	sort.Slice(items, func(i, j int) bool { return items[i].Path < items[j].Path })
	return items
}

func visibleValueMap(values map[string]any) []appliedValue {
	items := []appliedValue{}
	flattenValues("", values, &items)
	sort.Slice(items, func(i, j int) bool { return items[i].Path < items[j].Path })
	for index := range items {
		if items[index].Value == "••••••••" {
			items[index].Redacted = true
		}
	}
	return items
}
func mergeValues(target, source map[string]any) {
	for key, value := range source {
		if nested, ok := value.(map[string]any); ok {
			current, _ := target[key].(map[string]any)
			if current == nil {
				current = map[string]any{}
			}
			mergeValues(current, nested)
			target[key] = current
		} else {
			target[key] = value
		}
	}
}
func flattenValues(prefix string, value any, items *[]appliedValue) {
	if object, ok := value.(map[string]any); ok {
		keys := make([]string, 0, len(object))
		for key := range object {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			path := key
			if prefix != "" {
				path = prefix + "." + key
			}
			if sensitivePath(path) {
				*items = append(*items, appliedValue{Path: path, Value: "••••••••", Redacted: true})
			} else {
				flattenValues(path, object[key], items)
			}
		}
		return
	}
	*items = append(*items, appliedValue{Path: prefix, Value: value})
}
func sensitivePath(path string) bool {
	lower := strings.ToLower(path)
	for _, part := range []string{"password", "passwd", "secret", "token", "apikey", "api_key", "privatekey", "private_key", "credential"} {
		if strings.Contains(lower, part) {
			return true
		}
	}
	return false
}
func shortRevision(value string) string {
	if len(value) > 10 {
		return value[:10]
	}
	if value == "" {
		return "default"
	}
	return value
}
