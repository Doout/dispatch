package agentruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/deploy"
	"github.com/doout/dispatch/internal/remoteruntime"
	"github.com/doout/dispatch/internal/routing"
	"github.com/doout/dispatch/internal/runtimecontract"
)

func leasedJob() remoteruntime.LeasedJob {
	r := remoteruntime.NewRequest(runtimecontract.Deploy, core.Deployment{ID: "deployment", AppID: "app", SpecDigest: "spec"}, core.App{ID: "app", ProjectID: "project", ServerID: "server", BuildType: core.BuildTypeCompose, ComposeContent: "services: {}"}, core.Server{ID: "server", AgentNodeID: "node", Runtime: core.ServerRuntimeDocker})
	r.Inputs.SourceCredential = "never-persist-this-token"
	raw, _ := json.Marshal(r)
	hash := sha256.Sum256(raw)
	return remoteruntime.LeasedJob{ID: "operation", Attempt: 1, Digest: hex.EncodeToString(hash[:]), LeaseToken: "lease", ExpiresAt: time.Now().Add(time.Hour), Request: r}
}
func workerFixture(t *testing.T) *Worker {
	t.Helper()
	w, err := Open(filepath.Join(t.TempDir(), "private"), "node")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })
	w.hasWorkload = func(context.Context, remoteruntime.Request) (bool, error) { return false, nil }
	return w
}
func TestWorkerReplaysDurableResultWithoutEffects(t *testing.T) {
	w := workerFixture(t)
	job := leasedJob()
	calls := 0
	w.execute = func(context.Context, remoteruntime.Request, deploy.Progress) remoteruntime.Result {
		calls++
		return remoteruntime.Result{State: "succeeded", Logs: job.Request.Inputs.SourceCredential}
	}
	result := w.Run(context.Background(), job, nil)
	if result.State != "succeeded" || strings.Contains(result.Logs, job.Request.Inputs.SourceCredential) {
		t.Fatalf("result %#v", result)
	}
	dir := w.directory
	w.Close()
	reopened, err := Open(dir, "node")
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reopened.execute = func(context.Context, remoteruntime.Request, deploy.Progress) remoteruntime.Result {
		calls++
		return remoteruntime.Result{State: "failed"}
	}
	result = reopened.Run(context.Background(), job, nil)
	if result.State != "succeeded" || calls != 1 {
		t.Fatalf("replayed effects: %#v %d", result, calls)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "receipt-operation.json"))
	if strings.Contains(string(raw), job.Request.Inputs.SourceCredential) || strings.Contains(string(raw), "[redacted]") {
		t.Fatal("receipt result was not encrypted")
	}
}
func TestWorkerInterruptedMutationIsNeverRepeated(t *testing.T) {
	w := workerFixture(t)
	job := leasedJob()
	raw, _ := json.Marshal(receipt{Digest: job.Digest, State: "running"})
	if err := w.save("receipt-"+job.ID+".json", raw); err != nil {
		t.Fatal(err)
	}
	w.execute = func(context.Context, remoteruntime.Request, deploy.Progress) remoteruntime.Result {
		t.Fatal("interrupted operation repeated")
		return remoteruntime.Result{}
	}
	result := w.Run(context.Background(), job, nil)
	if result.Code != runtimecontract.Uncertain {
		t.Fatalf("result %#v", result)
	}
}
func TestWorkerRejectsInvalidOwnershipCancellationAndChangedPayload(t *testing.T) {
	for _, kind := range []string{"node", "digest", "cancel", "expired", "owner", "protocol"} {
		t.Run(kind, func(t *testing.T) {
			w := workerFixture(t)
			job := leasedJob()
			switch kind {
			case "node":
				job.Request.Server.AgentNodeID = "another"
			case "digest":
				job.Request.Inputs.ComposeContent = "changed"
			case "cancel":
				job.CancelRequested = true
			case "expired":
				job.ExpiresAt = time.Now().Add(-time.Second)
			case "owner":
				if err := w.save("owner-app", []byte("other-project:server")); err != nil {
					t.Fatal(err)
				}
			case "protocol":
				job.Request.APIVersion = "unknown"
			}
			w.execute = func(context.Context, remoteruntime.Request, deploy.Progress) remoteruntime.Result {
				t.Fatal("unsafe execution")
				return remoteruntime.Result{}
			}
			if result := w.Run(context.Background(), job, nil); result.State == "succeeded" {
				t.Fatal("invalid job accepted")
			}
		})
	}
}
func TestWorkerExclusiveLockAndNodeBinding(t *testing.T) {
	w := workerFixture(t)
	if other, err := Open(w.directory, "node"); err == nil {
		other.Close()
		t.Fatal("two workers own one journal")
	}
	dir := w.directory
	w.Close()
	if other, err := Open(dir, "other"); err == nil {
		other.Close()
		t.Fatal("journal inherited by different node")
	}
}

func TestInterruptedDockerMutationKeepsUnknownReceiptAcrossRestart(t *testing.T) {
	for _, operation := range []runtimecontract.Operation{runtimecontract.Start, runtimecontract.Stop} {
		for _, interruption := range []string{"cancel", "deadline"} {
			t.Run(string(operation)+"/"+interruption, func(t *testing.T) {
				w := workerFixture(t)
				job := leasedJob()
				job.Request.Operation = operation
				raw, _ := json.Marshal(job.Request)
				digest := sha256.Sum256(raw)
				job.Digest = hex.EncodeToString(digest[:])
				ctx, cancel := context.WithCancel(context.Background())
				if interruption == "deadline" {
					ctx, cancel = context.WithTimeout(context.Background(), 50*time.Millisecond)
				}
				defer cancel()
				container := strings.Repeat("a", 64)
				mutations := 0
				engine := dockerEngine{command: func(commandCtx context.Context, args ...string) (string, error) {
					switch args[0] {
					case "ps":
						return container, nil
					case "inspect":
						return `{"id":"` + container + `","applicationId":"app","state":"running"}`, nil
					case "start", "stop":
						mutations++
						if interruption == "cancel" {
							cancel()
						}
						<-commandCtx.Done()
						return "", errors.New("signal: killed")
					default:
						t.Fatalf("unexpected Docker command %v", args)
						return "", nil
					}
				}}
				w.execute = engine.Execute
				result := w.Run(ctx, job, nil)
				if result.State != "unknown" || result.Code != runtimecontract.Uncertain || mutations != 1 {
					t.Fatalf("interruption released the mutation: %#v calls=%d", result, mutations)
				}
				directory := w.directory
				w.Close()
				reopened, err := Open(directory, "node")
				if err != nil {
					t.Fatal(err)
				}
				defer reopened.Close()
				reopened.execute = func(context.Context, remoteruntime.Request, deploy.Progress) remoteruntime.Result {
					t.Fatal("interrupted Docker mutation was replayed")
					return remoteruntime.Result{}
				}
				replay := reopened.Run(context.Background(), job, nil)
				if replay.State != "unknown" || replay.Code != runtimecontract.Uncertain {
					t.Fatalf("restart lost the unknown outcome: %#v", replay)
				}
			})
		}
	}
}

func TestWorkerMissingJournalOnReofferedOperationNeverExecutes(t *testing.T) {
	w := workerFixture(t)
	job := leasedJob()
	job.Attempt = 2
	w.execute = func(context.Context, remoteruntime.Request, deploy.Progress) remoteruntime.Result {
		t.Fatal("a reoffered operation executed without its original journal")
		return remoteruntime.Result{}
	}
	result := w.Run(context.Background(), job, nil)
	if result.State != "unknown" || result.Code != runtimecontract.Uncertain {
		t.Fatalf("missing journal did not retain uncertainty: %#v", result)
	}
}

func TestWorkerLogsRequireCompleteHistoryAfterStateLoss(t *testing.T) {
	w := workerFixture(t)
	ctx := context.Background()
	job := leasedJob()
	// Inspection after state loss must not create an empty history that unlocks logs.
	job.Request.Operation = runtimecontract.Inspect
	if _, err := w.redactor(ctx, job.Request); err != nil {
		t.Fatal(err)
	}
	job.Request.Operation = runtimecontract.Logs
	if _, err := w.redactor(ctx, job.Request); err == nil {
		t.Fatal("inspection made missing credential history appear complete")
	}
	fresh := workerFixture(t)
	fresh.hasWorkload = func(context.Context, remoteruntime.Request) (bool, error) { return true, nil }
	job.Request.Operation = runtimecontract.Deploy
	if _, err := fresh.redactor(ctx, job.Request); err != nil {
		t.Fatal(err)
	}
	job.Request.Operation = runtimecontract.Logs
	if _, err := fresh.redactor(ctx, job.Request); err == nil {
		t.Fatal("replacement deployment exposed logs of an older workload without history")
	}
}

func TestWorkerPersistsRouteEvidenceOnFailureAndReplaysIt(t *testing.T) {
	w := workerFixture(t)
	job := leasedJob()
	job.Request.Application.ContainerPort = 8080
	job.Request.Server.Routing = &core.RoutingConfig{BaseDomain: "apps.example.com"}
	raw, _ := json.Marshal(job.Request)
	digest := sha256.Sum256(raw)
	job.Digest = hex.EncodeToString(digest[:])
	calls := 0
	w.execute = func(ctx context.Context, r remoteruntime.Request, _ deploy.Progress) remoteruntime.Result {
		calls++
		route, err := routing.Plan(r.Deployment, r.Application, r.Server)
		if err != nil {
			t.Fatal(err)
		}
		route.State = "preparing"
		if err = deploy.ReportRoute(ctx, *route); err != nil {
			t.Fatal(err)
		}
		return failure(runtimecontract.Unavailable, "candidate health failed")
	}
	for i := 0; i < 2; i++ {
		result := w.Run(context.Background(), job, nil)
		if result.State != "failed" || result.Route == nil || result.Route.State != "preparing" || result.Route.AppID != "app" {
			t.Fatal("failure lost route evidence", result)
		}
	}
	if calls != 1 {
		t.Fatal("route retry replayed mutation")
	}
}
func TestWorkerRoutingDirectoryIsOperatorConfiguredAbsolutePath(t *testing.T) {
	if w, err := Open(t.TempDir(), "node", Options{RoutingDirectory: "relative"}); err == nil {
		w.Close()
		t.Fatal("relative publisher directory accepted")
	}
}
