package provision

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/bootstrap"
	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/edge"
	"github.com/doout/dispatch/internal/provider"
	"github.com/doout/dispatch/internal/provider/mock"
	"github.com/doout/dispatch/internal/remoteruntime"
	"github.com/doout/dispatch/internal/store"
)

func enableMachineActions(t *testing.T, f *lifecycleFixture) {
	t.Helper()
	p, e := f.data.GetInfrastructureProvider(context.Background(), f.input.ProviderID)
	if e != nil {
		t.Fatal(e)
	}
	caps := append(append([]string{}, p.Capabilities...), provider.CapabilityPowerStart, provider.CapabilityPowerStop, provider.CapabilityPowerReboot)
	if strings.Contains(strings.Join(caps, ","), provider.CapabilityRestore) {
		caps = append(caps, provider.CapabilityPromote)
	}
	if _, e = f.m.Update(context.Background(), p.ID, Registration{Name: p.Name, Endpoint: p.Endpoint, Enabled: true, Revision: p.Revision, Capabilities: caps}); e != nil {
		t.Fatal(e)
	}
}
func freshRuntime(t *testing.T, f *lifecycleFixture, id string) {
	t.Helper()
	ctx := context.Background()
	s, e := f.data.GetManagedServer(ctx, id)
	if e != nil {
		t.Fatal(e)
	}
	node, e := f.data.GetPrivateNetwork(ctx, s.NodeID)
	if e != nil {
		t.Fatal(e)
	}
	f.now = f.now.Add(time.Second)
	node.Details["runtimeCheckedAt"] = f.now.Format(time.RFC3339Nano)
	node.Details["runtimeCapabilities"] = "deploy,inspect"
	node.Details["runtimeVersion"] = remoteruntime.APIVersion
	if e = f.data.UpdatePrivateNetwork(ctx, node); e != nil {
		t.Fatal(e)
	}
	if e = f.m.RefreshReadiness(ctx); e != nil {
		t.Fatal(e)
	}
}
func enrollMachine(t *testing.T, f *lifecycleFixture, id string) {
	t.Helper()
	ctx := context.Background()
	s, _ := f.data.GetManagedServer(ctx, id)
	token := ""
	if s.BootstrapID != "" {
		b, e := f.data.GetTargetBootstrap(ctx, s.BootstrapID)
		if e != nil {
			t.Fatal(e)
		}
		raw, e := f.m.Vault.Decrypt("target-bootstrap:"+b.ID, b.EncryptedInput)
		if e != nil {
			t.Fatal(e)
		}
		var saved struct {
			ClaimToken string `json:"claimToken"`
		}
		if json.Unmarshal(raw, &saved) != nil {
			t.Fatal("bootstrap payload")
		}
		claim, e := f.m.Bootstrap.Claim(ctx, b.ID, saved.ClaimToken)
		if e != nil {
			t.Fatal(e)
		}
		token = claim.Token
		node, _ := f.data.GetPrivateNetwork(ctx, s.NodeID)
		node.Details["agentArtifactSHA256"] = b.Plan.ArtifactSHA256
		node.LastVerifiedAt = &f.now
		if e = f.data.UpdatePrivateNetwork(ctx, node); e != nil {
			t.Fatal(e)
		}
	} else {
		enrollment, e := f.m.Enrollment(ctx, id)
		if e != nil {
			t.Fatal(e)
		}
		token = enrollment.Token
	}
	public := base64.RawURLEncoding.EncodeToString(ed25519.NewKeyFromSeed(make([]byte, 32)).Public().(ed25519.PublicKey))
	if _, e := edge.Enroll(ctx, f.data, s.NodeID, token, public, f.now); e != nil {
		t.Fatal(e)
	}
	freshRuntime(t, f, id)
}
func readyClone(t *testing.T) (*lifecycleFixture, core.ManagedServer, core.ManagedServer) {
	t.Helper()
	f, source := snapshotFixture(t)
	enableMachineActions(t, f)
	ctx := context.Background()
	r := captureReview(t, f, source)
	capture := acceptCapture(t, f, r, "action-capture")
	if o := f.finish(t, capture.Operation.ID); o.State != "succeeded" {
		t.Fatal(o)
	}
	root := t.TempDir()
	if e := os.WriteFile(filepath.Join(root, "linux-amd64"), []byte("pinned fixture"), 0600); e != nil {
		t.Fatal(e)
	}
	f.m.Bootstrap = bootstrap.Configured(f.data, f.m.Vault, "https://dispatch.example.com")
	f.m.Bootstrap.ArtifactRoot = root
	f.m.Bootstrap.Now = f.m.Now
	in := f.input
	in.ActorID = "actor"
	in.Name = "clone"
	in.Network = "mock-isolated"
	in.SourceSnapshotID = capture.Snapshot.ID
	in.Bootstrap = &core.TargetBootstrapPlan{Method: "cloud_init", Platform: "linux-amd64", ImageFamily: "ubuntu-24.04", InstallRuntime: true}
	review, e := f.m.ReviewCreate(ctx, in)
	if e != nil {
		t.Fatal(e)
	}
	accepted, e := f.m.AcceptCreate(ctx, "actor", Acceptance{ReviewID: review.ID, Digest: review.Digest, ConfirmName: review.Name, RequestKey: "action-clone"})
	if e != nil {
		t.Fatal(e)
	}
	if o := f.finish(t, accepted.Operation.ID); o.State != "succeeded" {
		t.Fatal(o)
	}
	enrollMachine(t, f, accepted.Server.ID)
	clone, e := f.data.GetManagedServer(ctx, accepted.Server.ID)
	if e != nil {
		t.Fatal(e)
	}
	return f, source, clone
}
func TestMachinePowerLostReplyRestartAndFreshReadiness(t *testing.T) {
	f := newLifecycleFixture(t)
	enableMachineActions(t, f)
	ctx := context.Background()
	_, created := f.accept(t)
	if o := f.finish(t, created.Operation.ID); o.State != "succeeded" {
		t.Fatal(o)
	}
	enrollMachine(t, f, created.Server.ID)
	s, _ := f.data.GetManagedServer(ctx, created.Server.ID)
	input := PowerInput{Action: "stop", Revision: s.Revision}
	accepted, e := f.m.Power(ctx, s.ID, "actor", "power-once", input)
	if e != nil {
		t.Fatal(e)
	}
	status, e := f.m.GetManaged(ctx, s.ID)
	if e != nil || status.Deployable || status.LatestOperation == nil || status.LatestOperation.ID != accepted.Operation.ID || status.LatestOperation.State != "pending" {
		t.Fatal("active power action was deployable or hidden", status, e)
	}
	again, e := f.m.Power(ctx, s.ID, "actor", "power-once", input)
	if e != nil || again.Operation.ID != accepted.Operation.ID {
		t.Fatal("acceptance replay", e)
	}
	if _, e = f.m.Power(ctx, s.ID, "actor", "power-once", PowerInput{Action: "start", Revision: input.Revision}); !errors.Is(e, store.ErrInfrastructureChanged) {
		t.Fatal("changed payload replay", e)
	}
	lost := false
	f.m.HTTPClient = &http.Client{Transport: upgradeTransport(func(r *http.Request) (*http.Response, error) {
		response, e := http.DefaultTransport.RoundTrip(r)
		if !lost && strings.HasSuffix(r.URL.Path, "/power") && r.Method == "POST" && e == nil {
			lost = true
			response.Body.Close()
			return nil, errors.New("lost action acknowledgement")
		}
		return response, e
	})}
	f.tick(t)
	reopened, e := store.Open(ctx, f.path)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	f.data = reopened
	f.m.Store = reopened
	if o := f.finish(t, accepted.Operation.ID); o.State != "succeeded" {
		t.Fatal(o)
	}
	status, e = f.m.GetManaged(ctx, s.ID)
	if e != nil || status.PowerState != provider.PowerStopped || status.Deployable || status.WaitState != "stopped" || status.LatestOperation == nil || status.LatestOperation.ID != accepted.Operation.ID {
		t.Fatal("stopped VM ready", status, e)
	}
	s, _ = f.data.GetManagedServer(ctx, s.ID)
	start, e := f.m.Power(ctx, s.ID, "actor", "start-once", PowerInput{Action: "start", Revision: s.Revision})
	if e != nil {
		t.Fatal(e)
	}
	if o := f.finish(t, start.Operation.ID); o.State != "succeeded" {
		t.Fatal(o)
	}
	status, e = f.m.GetManaged(ctx, s.ID)
	if e != nil || status.Deployable {
		t.Fatal("pre-start heartbeat reused", status, e)
	}
	freshRuntime(t, f, s.ID)
	status, e = f.m.GetManaged(ctx, s.ID)
	if e != nil || !status.Deployable {
		t.Fatal("fresh powered target unavailable", status, e)
	}
	saved, e := f.data.GetInfrastructureActionRequest(ctx, start.Operation.ID)
	if e != nil || strings.Contains(saved.EncryptedRequest, "expectedIdentity") {
		t.Fatal("action input not encrypted", e)
	}
}
func TestClonePromotionPreservesAncestryAndRequiresFreshRuntime(t *testing.T) {
	f, source, clone := readyClone(t)
	ctx := context.Background()
	inspect, e := f.m.InspectClone(ctx, clone.ID)
	if e != nil || !inspect.Verified || inspect.Server.Deployable || inspect.ApplicationIntegrity != "unverified" {
		t.Fatal(inspect, e)
	}
	clone = inspect.Server.ManagedServer
	input := PromotionInput{Network: "mock-private", Revision: clone.Revision, ConfirmName: clone.Name}
	accepted, e := f.m.Promote(ctx, clone.ID, "actor", "promote-once", input)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.data.GetServer(ctx, clone.ID); !errors.Is(e, store.ErrNotFound) {
		t.Fatal("published before network release")
	}
	lost := false
	f.m.HTTPClient = &http.Client{Transport: upgradeTransport(func(r *http.Request) (*http.Response, error) {
		response, e := http.DefaultTransport.RoundTrip(r)
		if !lost && strings.HasSuffix(r.URL.Path, "/promote") && r.Method == "POST" && e == nil {
			lost = true
			response.Body.Close()
			return nil, errors.New("lost promotion acknowledgement")
		}
		return response, e
	})}
	f.tick(t)
	if o := f.finish(t, accepted.Operation.ID); o.State != "succeeded" {
		t.Fatal(o)
	}
	status, e := f.m.GetManaged(ctx, clone.ID)
	if e != nil || status.PromotionState != "promoted" || status.SourceSnapshotID != clone.SourceSnapshotID || status.NodeID != clone.NodeID || status.Deployable {
		t.Fatal("invalid promoted readiness", status, e)
	}
	freshRuntime(t, f, clone.ID)
	status, e = f.m.GetManaged(ctx, clone.ID)
	if e != nil || !status.Deployable {
		t.Fatal("promoted clone unavailable", status, e)
	}
	target, e := f.data.GetServer(ctx, clone.ID)
	if e != nil || target.AgentNodeID != clone.NodeID || target.ProjectID != clone.ProjectID {
		t.Fatal("published another target", target, e)
	}
	original, e := f.data.GetManagedServer(ctx, source.ID)
	if e != nil || original.ResourceID != source.ResourceID || original.Revision != source.Revision {
		t.Fatal("promotion touched source", original, e)
	}
	again, e := f.m.Promote(ctx, clone.ID, "actor", "promote-once", input)
	if e != nil || again.Operation.ID != accepted.Operation.ID {
		t.Fatal("promotion retry identity", e)
	}
}
func TestClonePromotionChangedEnrollmentAndProviderEvidenceBlockPublication(t *testing.T) {
	for _, change := range []string{"enrollment", "identity", "release"} {
		t.Run(change, func(t *testing.T) {
			f, _, clone := readyClone(t)
			ctx := context.Background()
			accepted, e := f.m.Promote(ctx, clone.ID, "actor", "unsafe-promotion", PromotionInput{Network: "mock-private", Revision: clone.Revision, ConfirmName: clone.Name})
			if e != nil {
				t.Fatal(e)
			}
			f.tick(t)
			if change == "enrollment" {
				if e = f.data.RevokeEdgeCredential(ctx, clone.NodeID, f.now); e != nil {
					t.Fatal(e)
				}
			} else {
				f.m.HTTPClient = &http.Client{Transport: upgradeTransport(func(r *http.Request) (*http.Response, error) {
					response, e := http.DefaultTransport.RoundTrip(r)
					if e == nil && r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/v1/servers/") {
						var s provider.Server
						json.NewDecoder(response.Body).Decode(&s)
						response.Body.Close()
						if change == "identity" {
							s.MachineIdentity = "foreign-machine"
						} else if s.Promotion != nil {
							s.Promotion.QuarantineReleased = false
						}
						raw, _ := json.Marshal(s)
						response.Body = readCloser{strings.NewReader(string(raw))}
					}
					return response, e
				})}
			}
			if o := f.finish(t, accepted.Operation.ID); o.State != "unknown" {
				t.Fatal("unsafe promotion succeeded", o)
			}
			if _, e = f.data.GetServer(ctx, clone.ID); !errors.Is(e, store.ErrNotFound) {
				t.Fatal("unsafe clone published")
			}
		})
	}
}

func TestMachineActionDisableBeforeSubmissionAndInspectionRecovery(t *testing.T) {
	f := newLifecycleFixture(t)
	enableMachineActions(t, f)
	ctx := context.Background()
	_, created := f.accept(t)
	f.finish(t, created.Operation.ID)
	s, _ := f.data.GetManagedServer(ctx, created.Server.ID)
	accepted, e := f.m.Power(ctx, s.ID, "actor", "disable-action", PowerInput{Action: "stop", Revision: s.Revision})
	if e != nil {
		t.Fatal(e)
	}
	p, _ := f.data.GetInfrastructureProvider(ctx, s.ProviderID)
	if _, e = f.m.Update(ctx, p.ID, Registration{Name: p.Name, Endpoint: p.Endpoint, Revision: p.Revision, Enabled: false, Capabilities: p.Capabilities}); e != nil {
		t.Fatal(e)
	}
	if o := f.finish(t, accepted.Operation.ID); o.State != "unknown" {
		t.Fatal(o)
	}
	resource, e := f.adapter.Server(ctx, s.ResourceID)
	if e != nil || resource.PowerState != provider.PowerRunning || resource.PowerOperationID != "" {
		t.Fatal("disabled provider submitted action", resource, e)
	}
	if _, _, e = f.m.Adapter(ctx, p.ID, provider.CapabilityInspect, ""); e != nil {
		t.Fatal("disabled registration blocked inspection", e)
	}
	p, _ = f.data.GetInfrastructureProvider(ctx, p.ID)
	if _, e = f.m.Update(ctx, p.ID, Registration{Name: p.Name, Endpoint: p.Endpoint, Revision: p.Revision, Enabled: true, Capabilities: p.Capabilities}); e != nil {
		t.Fatal(e)
	}
	if e = f.m.ChangeOperation(ctx, accepted.Operation.ID, "retry"); e != nil {
		t.Fatal(e)
	}
	if o := f.finish(t, accepted.Operation.ID); o.State != "succeeded" {
		t.Fatal("original action did not resume", o)
	}
}
func TestMachineActionResolutionInspectsAcknowledgedFailure(t *testing.T) {
	f, _, clone := readyClone(t)
	ctx := context.Background()
	failed, e := mock.New(mock.Options{StateFile: f.mockPath, FailPromotion: true})
	if e != nil {
		t.Fatal(e)
	}
	f.adapter = failed
	accepted, e := f.m.Promote(ctx, clone.ID, "actor", "failed-action", PromotionInput{Network: "mock-private", Revision: clone.Revision, ConfirmName: clone.Name})
	if e != nil {
		t.Fatal(e)
	}
	if o := f.finish(t, accepted.Operation.ID); o.State != "unknown" {
		t.Fatal(o)
	}
	f.now = f.now.Add(time.Hour)
	if e = f.m.ChangeOperation(ctx, accepted.Operation.ID, "resolve"); e != nil {
		t.Fatal(e)
	}
	o, _ := f.data.GetInfrastructureOperation(ctx, accepted.Operation.ID)
	if o.State != "failed" || o.Stage != "poll" || !o.ExpiresAt.Before(f.now) {
		t.Fatal("resolution replaced operation or deadline", o)
	}
	s, _ := f.data.GetManagedServer(ctx, clone.ID)
	if s.PromotionState != "isolated" || s.SourceSnapshotID != clone.SourceSnapshotID {
		t.Fatal("failed promotion lost isolation", s)
	}
	if _, e = f.data.GetServer(ctx, clone.ID); !errors.Is(e, store.ErrNotFound) {
		t.Fatal("failed clone published")
	}
}
func TestMachineActionCancelledUnacknowledgedSubmissionRetainsLock(t *testing.T) {
	f := newLifecycleFixture(t)
	enableMachineActions(t, f)
	ctx := context.Background()
	_, created := f.accept(t)
	f.finish(t, created.Operation.ID)
	s, _ := f.data.GetManagedServer(ctx, created.Server.ID)
	accepted, e := f.m.Power(ctx, s.ID, "actor", "cancel-uncertain", PowerInput{Action: "stop", Revision: s.Revision})
	if e != nil {
		t.Fatal(e)
	}
	lost := false
	f.m.HTTPClient = &http.Client{Transport: upgradeTransport(func(r *http.Request) (*http.Response, error) {
		response, e := http.DefaultTransport.RoundTrip(r)
		if !lost && r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/power") && e == nil {
			lost = true
			response.Body.Close()
			return nil, errors.New("lost acknowledgement")
		}
		return response, e
	})}
	f.tick(t)
	if e = f.m.ChangeOperation(ctx, accepted.Operation.ID, "cancel"); e != nil {
		t.Fatal(e)
	}
	if o := f.finish(t, accepted.Operation.ID); o.State != "unknown" || !o.CancelRequested {
		t.Fatal("uncertain cancelled action unlocked", o)
	}
	if e = f.m.ChangeOperation(ctx, accepted.Operation.ID, "retry"); !errors.Is(e, store.ErrInfrastructureChanged) {
		t.Fatal("cancel authorized replay", e)
	}
	if e = f.m.ChangeOperation(ctx, accepted.Operation.ID, "resolve"); !errors.Is(e, store.ErrInfrastructureChanged) {
		t.Fatal("unacknowledged operation inspected as known", e)
	}
	s, _ = f.data.GetManagedServer(ctx, s.ID)
	if _, e = f.m.Power(ctx, s.ID, "actor", "replacement", PowerInput{Action: "start", Revision: s.Revision}); e == nil {
		t.Fatal("replacement mutation bypassed uncertain lock")
	}
}

func TestUnknownUnacknowledgedPowerRetriesOriginalProviderKey(t *testing.T) {
	f := newLifecycleFixture(t)
	enableMachineActions(t, f)
	ctx := context.Background()
	_, created := f.accept(t)
	f.finish(t, created.Operation.ID)
	s, _ := f.data.GetManagedServer(ctx, created.Server.ID)
	accepted, e := f.m.Power(ctx, s.ID, "actor", "unknown-original", PowerInput{Action: "stop", Revision: s.Revision})
	if e != nil {
		t.Fatal(e)
	}
	damaged := false
	f.m.HTTPClient = &http.Client{Transport: upgradeTransport(func(r *http.Request) (*http.Response, error) {
		response, e := http.DefaultTransport.RoundTrip(r)
		if !damaged && r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/power") && e == nil {
			damaged = true
			response.Body.Close()
			response.Body = readCloser{strings.NewReader(`{"id":"untrusted","state":"invalid","resourceId":"untrusted"}`)}
		}
		return response, e
	})}
	if o := f.finish(t, accepted.Operation.ID); o.State != "unknown" || o.ProviderOperationID != "" || o.Stage != "submitting" {
		t.Fatal("invalid acknowledgement accepted", o)
	}
	status, e := f.m.GetManaged(ctx, s.ID)
	if e != nil || status.WaitState != "unknown" || status.LatestOperation == nil || status.LatestOperation.ID != accepted.Operation.ID || status.LatestOperation.State != "unknown" {
		t.Fatal("unknown machine action hidden by original allocation", status, e)
	}
	if e = f.m.ChangeOperation(ctx, accepted.Operation.ID, "retry"); e != nil {
		t.Fatal(e)
	}
	if o := f.finish(t, accepted.Operation.ID); o.State != "succeeded" {
		t.Fatal("original-key retry did not converge", o)
	}
	ops, e := f.data.ListInfrastructureOperations(ctx, s.ID)
	if e != nil || len(ops) != 2 || ops[1].ID != accepted.Operation.ID {
		t.Fatal("replacement operation was created", ops, e)
	}
}
func TestMachineActionExpiredLeaseCannotPublishCompletion(t *testing.T) {
	f := newLifecycleFixture(t)
	enableMachineActions(t, f)
	ctx := context.Background()
	_, created := f.accept(t)
	f.finish(t, created.Operation.ID)
	s, _ := f.data.GetManagedServer(ctx, created.Server.ID)
	accepted, e := f.m.Power(ctx, s.ID, "actor", "lease-fence-action", PowerInput{Action: "stop", Revision: s.Revision})
	if e != nil {
		t.Fatal(e)
	}
	lease, e := f.data.LeaseInfrastructureOperation(ctx, f.now, time.Second)
	if e != nil || lease == nil {
		t.Fatal(e)
	}
	f.now = f.now.Add(2 * time.Second)
	lease.State = "succeeded"
	current, _ := f.data.GetManagedServer(ctx, s.ID)
	current.Revision++
	current.PowerState = provider.PowerStopped
	if e = f.data.CompleteInfrastructureAction(ctx, current, *lease, core.EdgeCredential{}, f.now, nil); !errors.Is(e, store.ErrInfrastructureChanged) {
		t.Fatal("expired worker committed machine state", e)
	}
	saved, e := f.data.GetInfrastructureOperation(ctx, accepted.Operation.ID)
	if e != nil || saved.State != "pending" {
		t.Fatal("expired completion changed journal", saved, e)
	}
	machine, _ := f.data.GetManagedServer(ctx, s.ID)
	if machine.PowerState != provider.PowerTransitioning {
		t.Fatal("expired completion published power", machine)
	}
}

func TestFailedPowerResolutionRequiresStableRunningOrStoppedEvidence(t *testing.T) {
	for _, state := range []string{provider.PowerUnknown, "", provider.PowerRunning, provider.PowerStopped} {
		t.Run("state-"+state, func(t *testing.T) {
			f := newLifecycleFixture(t)
			enableMachineActions(t, f)
			ctx := context.Background()
			_, created := f.accept(t)
			f.finish(t, created.Operation.ID)
			fault, e := mock.New(mock.Options{StateFile: f.mockPath, FailPower: true})
			if e != nil {
				t.Fatal(e)
			}
			f.adapter = fault
			s, _ := f.data.GetManagedServer(ctx, created.Server.ID)
			accepted, e := f.m.Power(ctx, s.ID, "actor", "failed-power-resolution", PowerInput{Action: "stop", Revision: s.Revision})
			if e != nil {
				t.Fatal(e)
			}
			if o := f.finish(t, accepted.Operation.ID); o.State != "unknown" {
				t.Fatal(o)
			}
			f.m.HTTPClient = &http.Client{Transport: upgradeTransport(func(r *http.Request) (*http.Response, error) {
				response, e := http.DefaultTransport.RoundTrip(r)
				if e == nil && r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/v1/servers/") {
					var resource provider.Server
					json.NewDecoder(response.Body).Decode(&resource)
					response.Body.Close()
					resource.PowerState = state
					raw, _ := json.Marshal(resource)
					response.Body = readCloser{strings.NewReader(string(raw))}
				}
				return response, e
			})}
			if e = f.m.ChangeOperation(ctx, accepted.Operation.ID, "resolve"); e != nil {
				t.Fatal(e)
			}
			original, _ := f.data.GetInfrastructureOperation(ctx, accepted.Operation.ID)
			stable := state == provider.PowerRunning || state == provider.PowerStopped
			if stable && original.State != "failed" || !stable && original.State != "unknown" {
				t.Fatal("failure resolved without stable machine evidence", original, state)
			}
			if !stable {
				current, _ := f.data.GetManagedServer(ctx, s.ID)
				if _, e = f.m.Power(ctx, s.ID, "actor", "replace-unresolved", PowerInput{Action: "start", Revision: current.Revision}); e == nil {
					t.Fatal("uncertain machine allowed replacement power action")
				}
			}
		})
	}
}
