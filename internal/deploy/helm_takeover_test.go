package deploy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/doout/dispatch/internal/core"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/storage/driver"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestHelmTakeover(t *testing.T) {
	for _, storage := range []string{"secret", "configmap"} {
		t.Run(storage, func(t *testing.T) {
			ctx := context.Background()
			k := fake.NewSimpleClientset()
			old := core.App{ID: "previous", ProjectID: "previous-project", HelmRelease: "slot1"}
			app := core.App{ID: "current", ProjectID: "current-project", HelmRelease: "slot1"}
			server := core.Server{ID: "new-target", Name: "Cluster", Kubernetes: &core.KubernetesServerConfig{Namespace: "slots"}}
			meta := newHelmDeploymentMetadata(old, core.Deployment{ID: "historical-deployment", SpecDigest: "historical-digest"})
			buf, err := meta.Run(bytes.NewBufferString("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: config\ndata:\n  chart-value: previous\n"))
			if err != nil {
				t.Fatal(err)
			}
			var db driver.Driver = driver.NewSecrets(k.CoreV1().Secrets("slots"))
			if storage == "configmap" {
				db = driver.NewConfigMaps(k.CoreV1().ConfigMaps("slots"))
			}
			key := func(v int) string { return fmt.Sprintf("sh.helm.release.v1.slot1.v%d", v) }
			create := func(v int, status release.Status) {
				t.Helper()
				r := &release.Release{Name: "slot1", Namespace: "slots", Version: v, Info: &release.Info{Status: status, Description: meta.description()}, Labels: meta.labels(), Manifest: buf.String()}
				if err := db.Create(key(v), r); err != nil {
					t.Fatal(err)
				}
			}
			create(431, release.StatusSuperseded)
			create(432, release.StatusDeployed)
			allow := func(id string) error {
				if id != old.ID {
					return errors.New("wrong previous owner")
				}
				return nil
			}
			read := func(version string) (HelmTakeoverReview, error) {
				return takeoverRelease(ctx, k, app, server, "slots", storage, version, allow)
			}
			k.ClearActions()
			review, err := read("")
			if err != nil || review.Revision != 432 || review.PreviousAppID != old.ID || review.PreviousProjectID != old.ProjectID {
				t.Fatal("inspect", review, err)
			}
			if _, err := takeoverRelease(ctx, k, app, server, "slots", storage, review.Version, func(string) error { return errors.New("still managed") }); err == nil {
				t.Fatal("accepted active owner")
			}
			if _, err := read(strings.Repeat("a", 64)); err == nil {
				t.Fatal("accepted stale review")
			}
			for _, action := range k.Actions() {
				if action.GetVerb() != "list" {
					t.Fatal("unexpected action before confirmation", action)
				}
			}
			k.ClearActions()
			if _, err = read(review.Version); err != nil {
				t.Fatal(err)
			}
			if actions := k.Actions(); len(actions) != 2 || actions[1].GetVerb() != "update" {
				t.Fatal("expected single write", actions)
			}
			current, err := db.Get(key(432))
			if err != nil || requireHelmReleaseOwner(current, app, "slots") != nil || requireHelmReleaseOwner(current, old, "slots") == nil || !strings.Contains(current.Info.Description, "historical-digest") {
				t.Fatal("transferred revision", err)
			}
			current.Info.Description = "Rollback to 432"
			if requireHelmReleaseOwner(current, app, "slots") != nil || !strings.Contains(current.Manifest, "chart-value: previous") {
				t.Fatal("lost manifest provenance or chart values")
			}
			older, err := db.Get(key(431))
			if err != nil || older.Manifest != buf.String() || older.Info.Description != meta.description() {
				t.Fatal("superseded history changed", err)
			}
			var object metav1.Object
			if storage == "secret" {
				object, err = k.CoreV1().Secrets("slots").Get(ctx, key(432), metav1.GetOptions{})
			} else {
				object, err = k.CoreV1().ConfigMaps("slots").Get(ctx, key(432), metav1.GetOptions{})
			}
			if err != nil || object.GetAnnotations()["dispatch.app/transferred-from-app"] != old.ID {
				t.Fatal("missing transfer evidence", err)
			}
			if _, err = read(review.Version); err == nil {
				t.Fatal("accepted consumed review")
			}
			create(433, release.StatusPendingUpgrade)
			if _, err = read(""); err == nil || !strings.Contains(err.Error(), "must be deployed") {
				t.Fatal("accepted pending revision", err)
			}
		})
	}
}
