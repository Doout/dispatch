package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/store"
)

func retentionFixture(t *testing.T) (*store.SQLStore, *Service, DockerExecutor, core.App, core.Server, core.RetentionPolicy) {
	t.Helper()
	data, docker, app, server, _ := runtimeFixture(t, core.BuildTypeDockerfile)
	ctx := context.Background()
	now := time.Now().UTC()
	for i := 0; i < 4; i++ {
		at := now.AddDate(0, 0, -30+i)
		d := core.Deployment{ID: fmt.Sprintf("retained-%d", i), AppID: app.ID, State: core.DeploymentSucceeded, CreatedAt: at, FinishedAt: &at, Snapshot: core.DeploymentSnapshot{TargetID: server.ID}}
		if err := data.CreateDeployment(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	p := core.RetentionPolicy{ProjectID: app.ProjectID, LogDays: 30, RunDays: 90, KeepRuns: 5, ImageDays: 7, StoppedRevisionDays: 7, KeepRollbackRevisions: 2}
	if err := data.SaveRetentionPolicy(ctx, p); err != nil {
		t.Fatal(err)
	}
	svc := NewService(data, SimulationExecutor{})
	return data, svc, docker, app, server, p
}
func TestRuntimeRetentionReviewsProtectReferencesAndRetireRollback(t *testing.T) {
	ctx := context.Background()
	data, svc, _, app, server, p := retentionFixture(t)
	if err := data.SaveDriftBaseline(ctx, core.DriftBaseline{DeploymentID: "retained-1", AppID: app.ID, ServerID: server.ID, Ciphertext: "private"}); err != nil {
		t.Fatal(err)
	}
	r, err := svc.PreviewRuntimeRetention(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	eligible := []string{}
	for _, item := range r.Items {
		if len(item.Protected) == 0 {
			eligible = append(eligible, item.DeploymentID)
		}
	}
	if strings.Join(eligible, ",") != "retained-0,retained-2" {
		t.Fatalf("wrong candidates %v: %+v", eligible, r)
	}
	if _, err = svc.ApplyRuntimeRetention(ctx, p, r.ID, "altered"); !errors.Is(err, store.ErrRuntimeRetentionChanged) {
		t.Fatalf("altered review accepted: %v", err)
	}
	applied, err := svc.ApplyRuntimeRetention(ctx, p, r.ID, r.Digest)
	if err != nil || applied.State != "succeeded" {
		t.Fatalf("apply %+v %v", applied, err)
	}
	source, err := data.GetDeployment(ctx, "retained-0")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	d := core.Deployment{ID: "rollback-retired", AppID: app.ID, State: core.DeploymentQueued, CreatedAt: at}
	if err = data.CreateRollbackDeployment(ctx, d, source, core.ReleaseAction{ID: "retired-action", ProjectID: app.ProjectID, CreatedAt: at}); !errors.Is(err, store.ErrRuntimeRetentionChanged) {
		t.Fatalf("retired rollback accepted: %v", err)
	}
	if err = data.SaveDriftBaseline(ctx, core.DriftBaseline{DeploymentID: source.ID, AppID: app.ID, ServerID: server.ID}); !errors.Is(err, store.ErrRuntimeRetentionChanged) {
		t.Fatalf("retired source acquired baseline: %v", err)
	}
	again, err := svc.ApplyRuntimeRetention(ctx, p, r.ID, r.Digest)
	if err != nil || len(again.Results) != 2 {
		t.Fatalf("retry changed receipt %+v %v", again, err)
	}
	next, err := svc.PreviewRuntimeRetention(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range next.Items {
		if item.DeploymentID == "retained-0" || item.DeploymentID == "retained-2" {
			t.Fatal("retired revision offered again")
		}
	}
}
func TestRuntimeRetentionRejectsPolicyAndNewReferences(t *testing.T) {
	ctx := context.Background()
	data, svc, _, app, server, p := retentionFixture(t)
	r, err := svc.PreviewRuntimeRetention(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	changed := p
	changed.StoppedRevisionDays = 60
	if err = data.SaveRetentionPolicy(ctx, changed); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.ApplyRuntimeRetention(ctx, p, r.ID, r.Digest); !errors.Is(err, store.ErrRetentionPolicyChanged) {
		t.Fatalf("stale policy accepted: %v", err)
	}
	if err = data.SaveRetentionPolicy(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err = data.SaveDriftBaseline(ctx, core.DriftBaseline{DeploymentID: "retained-0", AppID: app.ID, ServerID: server.ID, Ciphertext: "private"}); err != nil {
		t.Fatal(err)
	}
	applied, err := svc.ApplyRuntimeRetention(ctx, p, r.ID, r.Digest)
	if err != nil || applied.State != "partial" {
		t.Fatalf("new reference not protected: %+v %v", applied, err)
	}
	for _, result := range applied.Results {
		if strings.HasSuffix(result.Key, ":retained-0") && result.State != "protected" {
			t.Fatalf("baseline revision removed: %+v", result)
		}
	}
}

type interruptedRetention struct {
	RuntimeRetentionBackend
	fail  string
	calls []string
}

func (b *interruptedRetention) PruneRetention(ctx context.Context, app core.App, s core.Server, item core.RuntimeRetentionItem) (core.RuntimeRetentionOutcome, error) {
	b.calls = append(b.calls, item.Key)
	if strings.HasSuffix(item.Key, b.fail) && b.fail != "" {
		b.fail = ""
		return core.RuntimeRetentionOutcome{Key: item.Key, State: "failed", Message: "interrupted"}, errors.New("interrupted")
	}
	return b.RuntimeRetentionBackend.PruneRetention(ctx, app, s, item)
}
func TestRuntimeRetentionPartialRetryKeepsOriginalSet(t *testing.T) {
	ctx := context.Background()
	data, svc, _, app, _, p := retentionFixture(t)
	backend := &interruptedRetention{RuntimeRetentionBackend: svc.Retention, fail: ":retained-1"}
	svc.Retention = backend
	r, err := svc.PreviewRuntimeRetention(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	first, err := svc.ApplyRuntimeRetention(ctx, p, r.ID, r.Digest)
	if err != nil || first.State != "partial" {
		t.Fatalf("expected partial %+v %v", first, err)
	}
	at := time.Now().AddDate(0, 0, -60)
	if err = data.CreateDeployment(ctx, core.Deployment{ID: "unreviewed", AppID: app.ID, State: core.DeploymentFailed, CreatedAt: at}); err != nil {
		t.Fatal(err)
	}
	// Acceptance expiry does not prevent a bounded retry of a partial operation.
	first.ExpiresAt = time.Now().Add(-time.Hour)
	if err = data.UpdateRuntimeRetentionReview(ctx, first, true); err != nil {
		t.Fatal(err)
	}
	second, err := svc.ApplyRuntimeRetention(ctx, p, r.ID, r.Digest)
	if err != nil || second.State != "succeeded" {
		t.Fatalf("retry %+v %v", second, err)
	}
	counts := map[string]int{}
	for _, key := range backend.calls {
		counts[key]++
		if strings.Contains(key, "unreviewed") {
			t.Fatal("retry broadened deletion set")
		}
	}
	if counts["revision:runtime-server:retained-0"] != 1 || counts["revision:runtime-server:retained-1"] != 2 {
		t.Fatalf("completed work repeated: %v", counts)
	}
}

type retentionDockerFixture struct {
	containers    map[string]retentionContainer
	images        map[string]retentionImage
	commands      []string
	failContainer string
}

func (f *retentionDockerFixture) run(_ context.Context, _ io.Reader, out io.Writer, name string, args ...string) error {
	if name != "docker" {
		return errors.New("unexpected command")
	}
	f.commands = append(f.commands, strings.Join(args, " "))
	switch {
	case args[0] == "ps":
		for id := range f.containers {
			fmt.Fprintln(out, id)
		}
	case args[0] == "inspect":
		c, ok := f.containers[args[len(args)-1]]
		if !ok {
			return errors.New("absent")
		}
		json.NewEncoder(out).Encode(c)
	case args[0] == "image" && args[1] == "ls":
		for id := range f.images {
			fmt.Fprintln(out, id)
		}
	case args[0] == "image" && args[1] == "inspect":
		im, ok := f.images[args[len(args)-1]]
		if !ok {
			return errors.New("absent")
		}
		json.NewEncoder(out).Encode(im)
	case args[0] == "image" && args[1] == "rm":
		id := args[2]
		for _, c := range f.containers {
			if c.Image == id {
				return errors.New("in use")
			}
		}
		delete(f.images, id)
	case args[0] == "rm":
		if len(args) != 2 {
			return errors.New("unsafe removal flags")
		}
		if args[1] == f.failContainer {
			f.failContainer = ""
			return errors.New("interrupted")
		}
		if f.containers[args[1]].Running {
			return errors.New("running")
		}
		delete(f.containers, args[1])
	default:
		return fmt.Errorf("unexpected command: %v", args)
	}
	return nil
}
func TestDockerRuntimeRetentionProtectsSharedImagesAndRemovesOnlyReviewedObjects(t *testing.T) {
	ctx := context.Background()
	data, e, app, server, d := runtimeFixture(t, core.BuildTypeDockerfile)
	id := "sha256:" + strings.Repeat("a", 64)
	container := strings.Repeat("b", 64)
	foreign := strings.Repeat("c", 64)
	at := time.Now().AddDate(0, 0, -30)
	d.CreatedAt = at
	if err := e.saveArtifact(ctx, d, app, server, dockerArtifact{Version: 1, BuildType: app.BuildType, Images: map[string]string{"application": id}, Bindings: []core.ServiceRuntimeBinding{{Values: map[string]string{"password": "retained-secret"}}}}); err != nil {
		t.Fatal(err)
	}
	f := &retentionDockerFixture{containers: map[string]retentionContainer{container: {ID: container, App: app.ID, Deployment: d.ID, Image: id}}, images: map[string]retentionImage{id: {ID: id, Created: at, Tags: []string{"dispatch/app:old"}}}}
	e.run = f.run
	items, err := e.InspectRetention(ctx, app, server)
	if err != nil {
		t.Fatal(err)
	}
	var revision core.RuntimeRetentionItem
	for _, item := range items {
		if item.Kind == "revision" {
			revision = item
		}
		if item.Kind == "image" && len(item.Protected) == 0 {
			t.Fatal("retained image was offered")
		}
	}
	dir := e.artifactDirectory(app.ID, d.ID)
	if err = os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte("retained-secret"), 0600)
	f.containers[foreign] = retentionContainer{ID: foreign, App: "foreign", Deployment: "other", Image: id}
	out, err := e.PruneRetention(ctx, app, server, revision)
	if err != nil || out.State != "removed" {
		t.Fatalf("revision prune %+v %v", out, err)
	}
	if _, exists := f.containers[foreign]; !exists {
		t.Fatal("foreign container removed")
	}
	if _, err = os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("retained config not removed")
	}
	artifact, err := data.GetRuntimeArtifact(ctx, d.ID)
	if err != nil || !artifact.Metadata.Retired || artifact.Ciphertext != "" {
		t.Fatalf("private inputs retained: %+v %v", artifact, err)
	}
	items, err = e.InspectRetention(ctx, app, server)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || len(items[0].Protected) == 0 {
		t.Fatalf("shared image unprotected: %+v", items)
	}
	delete(f.containers, foreign)
	items, err = e.InspectRetention(ctx, app, server)
	if err != nil || len(items) != 1 || len(items[0].Protected) != 0 {
		t.Fatalf("image not eligible after references clear: %+v %v", items, err)
	}
	image := items[0]
	out, err = e.PruneRetention(ctx, app, server, image)
	if err != nil || out.State != "removed" {
		t.Fatalf("image cleanup %+v %v", out, err)
	}
	out, err = e.PruneRetention(ctx, app, server, image)
	if err != nil || out.State != "absent" {
		t.Fatalf("absent retry %+v %v", out, err)
	}
	for _, command := range f.commands {
		if strings.Contains(command, "volume") || strings.Contains(command, "network") || strings.Contains(command, "prune") || strings.Contains(command, "rm -") {
			t.Fatalf("unsafe cleanup: %s", command)
		}
	}
}
func TestDockerRuntimeRetentionRechecksRunningAndAddedContainers(t *testing.T) {
	ctx := context.Background()
	_, e, app, server, d := runtimeFixture(t, core.BuildTypeDockerfile)
	id := "sha256:" + strings.Repeat("a", 64)
	container := strings.Repeat("b", 64)
	if err := e.saveArtifact(ctx, d, app, server, dockerArtifact{Version: 1, BuildType: app.BuildType, Images: map[string]string{"application": id}}); err != nil {
		t.Fatal(err)
	}
	f := &retentionDockerFixture{containers: map[string]retentionContainer{container: {ID: container, App: app.ID, Deployment: d.ID, Image: id}}, images: map[string]retentionImage{id: {ID: id, Tags: []string{"dispatch/app:old"}}}}
	e.run = f.run
	items, err := e.InspectRetention(ctx, app, server)
	if err != nil {
		t.Fatal(err)
	}
	var item core.RuntimeRetentionItem
	for _, candidate := range items {
		if candidate.Kind == "revision" {
			item = candidate
		}
	}
	c := f.containers[container]
	c.Running = true
	f.containers[container] = c
	result, err := e.PruneRetention(ctx, app, server, item)
	if err != nil || result.State != "protected" {
		t.Fatalf("live revision removed %+v %v", result, err)
	}
	c.Running = false
	f.containers[container] = c
	added := strings.Repeat("c", 64)
	c.ID = added
	f.containers[added] = c
	result, err = e.PruneRetention(ctx, app, server, item)
	if err != nil || result.State != "protected" {
		t.Fatalf("expanded revision removed %+v %v", result, err)
	}
}
