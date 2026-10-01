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

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/deploy"
	"github.com/doout/dispatch/internal/remoteruntime"
	"github.com/doout/dispatch/internal/runtimecontract"
)

func withRequest(job remoteruntime.LeasedJob, request remoteruntime.Request) remoteruntime.LeasedJob {
	job.Request = request
	raw, _ := json.Marshal(request)
	digest := sha256.Sum256(raw)
	job.Digest = hex.EncodeToString(digest[:])
	return job
}

func TestWorkerPersistsHealthBeforePromotionAndReplaysEvidence(t *testing.T) {
	for _, interrupted := range []bool{false, true} {
		t.Run(map[bool]string{false: "failed-publication", true: "interrupted-publication"}[interrupted], func(t *testing.T) {
			w := workerFixture(t)
			job := leasedJob()
			policy, err := core.NormalizeHealthPolicy(core.HealthPolicy{})
			if err != nil {
				t.Fatal(err)
			}
			job.Request.Deployment.Health = core.DeploymentHealth{Policy: policy, State: "pending"}
			job = withRequest(job, job.Request)
			var beforePromotion []byte
			calls := 0
			w.execute = func(ctx context.Context, request remoteruntime.Request, _ deploy.Progress) remoteruntime.Result {
				calls++
				record, err := deploy.RunHealthPolicy(ctx, policy, func(context.Context, core.HealthCheck) deploy.HealthObservation {
					return deploy.HealthObservation{Passed: true}
				})
				if err != nil {
					t.Fatal(err)
				}
				record.Checks[0].Message = "safe " + request.Inputs.SourceCredential
				if err = deploy.ReportDeploymentHealth(ctx, request.Deployment, record); err != nil {
					t.Fatal(err)
				}
				// Publication is allowed only after this durable running receipt exists.
				beforePromotion, err = os.ReadFile(filepath.Join(w.directory, "receipt-"+job.ID+".json"))
				if err != nil {
					t.Fatal(err)
				}
				var receipt receipt
				if json.Unmarshal(beforePromotion, &receipt) != nil || receipt.State != "running" || receipt.Health == "" {
					t.Fatal("promotion preceded durable health evidence")
				}
				if strings.Contains(string(beforePromotion), request.Inputs.SourceCredential) {
					t.Fatal("health receipt exposed a credential")
				}
				raw, err := w.vault.Decrypt("health:"+job.ID, receipt.Health)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(raw), request.Inputs.SourceCredential) {
					t.Fatal("health diagnostics were not redacted")
				}
				return failure(runtimecontract.Failed, "route publication failed")
			}
			result := w.Run(context.Background(), job, nil)
			if result.State != "failed" || result.Health == nil || result.Health.State != "passed" {
				t.Fatalf("failure discarded completed health: %#v", result)
			}
			if interrupted {
				if err = w.save("receipt-"+job.ID+".json", beforePromotion); err != nil {
					t.Fatal(err)
				}
			}
			dir := w.directory
			w.Close()
			reopened, err := Open(dir, "node")
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			reopened.execute = func(context.Context, remoteruntime.Request, deploy.Progress) remoteruntime.Result {
				t.Fatal("replayed deployment effects")
				return remoteruntime.Result{}
			}
			result = reopened.Run(context.Background(), job, nil)
			if calls != 1 || result.Health == nil || result.Health.State != "passed" {
				t.Fatalf("receipt lost health evidence: %#v", result)
			}
			if interrupted && result.Code != runtimecontract.Uncertain {
				t.Fatal("interrupted publication was declared complete")
			}
		})
	}
}

func TestWorkerHealthPersistenceFailureBlocksPromotion(t *testing.T) {
	w := workerFixture(t)
	job := leasedJob()
	w.execute = func(ctx context.Context, request remoteruntime.Request, _ deploy.Progress) remoteruntime.Result {
		record, err := deploy.RunHealthPolicy(ctx, core.HealthPolicy{}, func(context.Context, core.HealthCheck) deploy.HealthObservation {
			return deploy.HealthObservation{Passed: true}
		})
		if err != nil {
			t.Fatal(err)
		}
		// A replaced journal directory cannot retain the health gate.
		old := w.directory
		w.directory = filepath.Join(old, "missing")
		err = deploy.ReportDeploymentHealth(ctx, request.Deployment, record)
		w.directory = old
		if err == nil {
			t.Fatal("promotion continued without durable evidence")
		}
		return failure(runtimecontract.Unavailable, "health evidence could not be saved")
	}
	result := w.Run(context.Background(), job, nil)
	if result.State == "succeeded" || result.Health != nil {
		t.Fatalf("unpersisted health accepted: %#v", result)
	}
}

func TestWorkerRejectsHealthForDifferentDeploymentOrPolicy(t *testing.T) {
	w := workerFixture(t)
	job := leasedJob()
	w.execute = func(ctx context.Context, request remoteruntime.Request, _ deploy.Progress) remoteruntime.Result {
		record, _ := deploy.RunHealthPolicy(ctx, core.HealthPolicy{}, func(context.Context, core.HealthCheck) deploy.HealthObservation {
			return deploy.HealthObservation{Passed: true}
		})
		other := request.Deployment
		other.ID = "another"
		if err := deploy.ReportDeploymentHealth(ctx, other, record); err == nil {
			t.Fatal("other deployment evidence accepted")
		}
		record.Policy.FailureThreshold++
		if err := deploy.ReportDeploymentHealth(ctx, request.Deployment, record); err == nil {
			t.Fatal("changed policy accepted")
		}
		return failure(runtimecontract.InvalidRequest, errors.New("invalid health evidence").Error())
	}
	if result := w.Run(context.Background(), job, nil); result.Health != nil {
		t.Fatal("invalid evidence retained")
	}
}
