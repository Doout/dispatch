package deploy

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/doout/dispatch/internal/core"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

type RuntimeStorage struct {
	Run        func(context.Context, io.Reader, io.Writer, string, ...string) error
	Kubernetes func(core.Server) (kubernetes.Interface, error)
}

type boundedStorageOutput struct{ strings.Builder }

func (b *boundedStorageOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 4<<20 {
		return 0, errors.New("storage inspection exceeds output limit")
	}
	return b.Builder.Write(p)
}
func (r RuntimeStorage) docker(ctx context.Context, args ...string) (string, error) {
	var out boundedStorageOutput
	err := (DockerExecutor{run: r.Run}).command(ctx, nil, &out, "docker", args...)
	return out.String(), err
}

func storageLabels(labels map[string]string) map[string]string {
	allowed := map[string]string{}
	for _, key := range []string{"dispatch.app", "dispatch.app/app-id", "dispatch.app/managed-by", "dispatch.project", "dispatch.managed-by", "dispatch.service-template", "dispatch.service-provision", "com.docker.compose.project", "meta.helm.sh/release-name", "meta.helm.sh/release-namespace", "app.kubernetes.io/managed-by"} {
		if value := labels[key]; value != "" {
			allowed[key] = value
		}
	}
	return allowed
}

func storageEvidence(labels map[string]string) string {
	raw, _ := json.Marshal(storageLabels(labels))
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}

func (r RuntimeStorage) Inspect(ctx context.Context, server core.Server) ([]core.StorageObservation, error) {
	if server.AgentNodeID != "" {
		return nil, errors.New("enrolled storage inspection requires its remote worker")
	}
	if core.IsKubernetesRuntime(server.Runtime) {
		return r.inspectKubernetes(ctx, server)
	}
	if server.Runtime != core.ServerRuntimeDocker || server.Address != "local" && server.Address != "localhost" && server.Address != "127.0.0.1" {
		return nil, errors.New("storage inspection requires a supported enrolled runtime")
	}
	names, err := r.docker(ctx, "volume", "ls", "--format", "{{.Name}}")
	if err != nil {
		return nil, err
	}
	items := []core.StorageObservation{}
	indexes := map[string]int{}
	if len(strings.Fields(names)) > 10000 {
		return nil, errors.New("too many storage objects")
	}
	for _, name := range strings.Fields(names) {
		data, err := r.docker(ctx, "volume", "inspect", "--format", "{{json .}}", name)
		if err != nil {
			return nil, err
		}
		var volume struct {
			Name, CreatedAt, Driver string
			Labels                  map[string]string
		}
		if err = json.Unmarshal([]byte(data), &volume); err != nil {
			return nil, err
		}
		if volume.Name != name || volume.CreatedAt == "" {
			return nil, errors.New("volume identity is unavailable")
		}
		item := core.StorageResource{Kind: "docker_volume", Name: name, Identity: volume.CreatedAt + ":" + volume.Driver, Evidence: storageEvidence(volume.Labels), Consumers: []core.StorageConsumer{}}
		indexes[name] = len(items)
		items = append(items, core.StorageObservation{Resource: item, Labels: storageLabels(volume.Labels)})
	}
	containers, err := r.docker(ctx, "ps", "-aq")
	if err != nil {
		return nil, err
	}
	if len(strings.Fields(containers)) > 10000 {
		return nil, errors.New("too many runtime consumers")
	}
	for _, id := range strings.Fields(containers) {
		data, err := r.docker(ctx, "inspect", "--format", `{"id":{{json .Id}},"active":{{json .State.Running}},"mounts":{{json .Mounts}}}`, id)
		if err != nil {
			return nil, err
		}
		var container struct {
			ID     string
			Active bool
			Mounts []struct{ Type, Name, Destination string }
		}
		if err = json.Unmarshal([]byte(data), &container); err != nil {
			return nil, err
		}
		for _, mount := range container.Mounts {
			if mount.Type != "volume" {
				continue
			}
			if i, ok := indexes[mount.Name]; ok {
				items[i].Resource.Consumers = append(items[i].Resource.Consumers, core.StorageConsumer{ID: "container:" + container.ID, Mount: mount.Destination, Active: container.Active})
			}
		}
	}
	return items, nil
}

func (r RuntimeStorage) kube(ctx context.Context, server core.Server) (kubernetes.Interface, func(), error) {
	if r.Kubernetes != nil {
		client, err := r.Kubernetes(server)
		return client, func() {}, err
	}
	prepared, cleanup, err := prepareKubernetesServer(ctx, server)
	if err != nil {
		return nil, func() {}, err
	}
	client, err := serviceKubeClient(prepared)
	if err != nil {
		cleanup()
		return nil, func() {}, err
	}
	return client, cleanup, nil
}

func (r RuntimeStorage) inspectKubernetes(ctx context.Context, server core.Server) ([]core.StorageObservation, error) {
	client, cleanup, err := r.kube(ctx, server)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	namespace := "default"
	if server.Kubernetes != nil && server.Kubernetes.Namespace != "" {
		namespace = server.Kubernetes.Namespace
	}
	claims, err := client.CoreV1().PersistentVolumeClaims(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	items := []core.StorageObservation{}
	indexes := map[string]int{}
	for _, claim := range claims.Items {
		labels := map[string]string{}
		for k, v := range claim.Labels {
			labels[k] = v
		}
		for _, key := range []string{"meta.helm.sh/release-name", "meta.helm.sh/release-namespace"} {
			labels[key] = claim.Annotations[key]
		}
		state := "present"
		if claim.DeletionTimestamp != nil {
			state = "deleting"
		}
		item := core.StorageResource{Kind: "kubernetes_pvc", Name: claim.Name, Namespace: claim.Namespace, Identity: string(claim.UID), Evidence: storageEvidence(labels), State: state, Consumers: []core.StorageConsumer{}}
		for _, owner := range claim.OwnerReferences {
			item.Consumers = append(item.Consumers, core.StorageConsumer{ID: "owner:" + owner.Kind + ":" + claim.Namespace + "/" + owner.Name, Active: true})
		}
		if item.Identity == "" {
			return nil, errors.New("PVC identity is unavailable")
		}
		indexes[claim.Namespace+"/"+claim.Name] = len(items)
		items = append(items, core.StorageObservation{Resource: item, Labels: storageLabels(labels)})
	}
	pods, err := client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for _, pod := range pods.Items {
		for _, volume := range pod.Spec.Volumes {
			if volume.PersistentVolumeClaim == nil {
				continue
			}
			if i, ok := indexes[pod.Namespace+"/"+volume.PersistentVolumeClaim.ClaimName]; ok {
				mount := volume.Name
				for _, container := range pod.Spec.Containers {
					for _, m := range container.VolumeMounts {
						if m.Name == volume.Name {
							mount = m.MountPath
						}
					}
				}
				items[i].Resource.Consumers = append(items[i].Resource.Consumers, core.StorageConsumer{ID: "pod:" + pod.Namespace + "/" + pod.Name, Mount: mount, Active: pod.Status.Phase != "Succeeded" && pod.Status.Phase != "Failed"})
			}
		}
	}
	// Workload references remain consumers even when scaled to zero or between pods.
	deployments, err := client.AppsV1().Deployments(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for _, v := range deployments.Items {
		for _, volume := range v.Spec.Template.Spec.Volumes {
			if volume.PersistentVolumeClaim != nil {
				if i, ok := indexes[v.Namespace+"/"+volume.PersistentVolumeClaim.ClaimName]; ok {
					items[i].Resource.Consumers = append(items[i].Resource.Consumers, core.StorageConsumer{ID: "deployment:" + v.Namespace + "/" + v.Name, Mount: volume.Name, Active: v.Spec.Replicas == nil || *v.Spec.Replicas > 0})
				}
			}
		}
	}
	stateful, err := client.AppsV1().StatefulSets(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for _, v := range stateful.Items {
		for _, volume := range v.Spec.Template.Spec.Volumes {
			if volume.PersistentVolumeClaim != nil {
				if i, ok := indexes[v.Namespace+"/"+volume.PersistentVolumeClaim.ClaimName]; ok {
					items[i].Resource.Consumers = append(items[i].Resource.Consumers, core.StorageConsumer{ID: "statefulset:" + v.Namespace + "/" + v.Name, Mount: volume.Name, Active: v.Spec.Replicas == nil || *v.Spec.Replicas > 0})
				}
			}
		}
		for _, claim := range v.Spec.VolumeClaimTemplates {
			for key, i := range indexes {
				if strings.HasPrefix(key, v.Namespace+"/"+claim.Name+"-"+v.Name+"-") {
					items[i].Resource.Consumers = append(items[i].Resource.Consumers, core.StorageConsumer{ID: "statefulset:" + v.Namespace + "/" + v.Name, Mount: claim.Name, Active: v.Spec.Replicas == nil || *v.Spec.Replicas > 0})
				}
			}
		}
	}
	daemons, err := client.AppsV1().DaemonSets(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for _, v := range daemons.Items {
		appendPVCReferences(items, indexes, "daemonset:"+v.Namespace+"/"+v.Name, v.Namespace, v.Spec.Template.Spec)
	}
	replicas, err := client.AppsV1().ReplicaSets(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for _, v := range replicas.Items {
		appendPVCReferences(items, indexes, "replicaset:"+v.Namespace+"/"+v.Name, v.Namespace, v.Spec.Template.Spec)
	}
	jobs, err := client.BatchV1().Jobs(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for _, v := range jobs.Items {
		appendPVCReferences(items, indexes, "job:"+v.Namespace+"/"+v.Name, v.Namespace, v.Spec.Template.Spec)
	}
	crons, err := client.BatchV1().CronJobs(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for _, v := range crons.Items {
		appendPVCReferences(items, indexes, "cronjob:"+v.Namespace+"/"+v.Name, v.Namespace, v.Spec.JobTemplate.Spec.Template.Spec)
	}
	return items, nil
}

func appendPVCReferences(items []core.StorageObservation, indexes map[string]int, id, namespace string, spec corev1.PodSpec) {
	for _, volume := range spec.Volumes {
		if volume.PersistentVolumeClaim != nil {
			if i, ok := indexes[namespace+"/"+volume.PersistentVolumeClaim.ClaimName]; ok {
				items[i].Resource.Consumers = append(items[i].Resource.Consumers, core.StorageConsumer{ID: id, Mount: volume.Name, Active: true})
			}
		}
	}
}

func (r RuntimeStorage) Delete(ctx context.Context, server core.Server, item core.StorageResource) error {
	if item.Kind == "kubernetes_pvc" && server.Kubernetes != nil {
		config := *server.Kubernetes
		config.Namespace = item.Namespace
		server.Kubernetes = &config
	}
	// Re-inspect immediately before mutation. A reused name or new consumer is not
	// authorization to delete the replacement object.
	observed, err := r.Inspect(ctx, server)
	if err != nil {
		return err
	}
	found := false
	for _, current := range observed {
		v := current.Resource
		if v.Kind == item.Kind && v.Namespace == item.Namespace && v.Name == item.Name {
			found = true
			if v.Identity != item.Identity || v.Evidence != item.Evidence || len(v.Consumers) > 0 {
				return errors.New("storage identity or consumers changed")
			}
		}
	}
	if !found {
		return nil
	}
	switch item.Kind {
	case "docker_volume":
		_, err = r.docker(ctx, "volume", "rm", item.Name)
		return err // Never force removal of in-use data.
	case "kubernetes_pvc":
		client, cleanup, err := r.kube(ctx, server)
		if err != nil {
			return err
		}
		defer cleanup()
		uid := types.UID(item.Identity)
		err = client.CoreV1().PersistentVolumeClaims(item.Namespace).Delete(ctx, item.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}})
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		_, err = client.CoreV1().PersistentVolumeClaims(item.Namespace).Get(ctx, item.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		return errors.New("PVC deletion is pending; reconcile its finalizers before retrying")
	default:
		return errors.New("storage deletion is unsupported by this driver")
	}
}
