# Kubernetes and K3s targets

Dispatch registers an existing cluster namespace for Helm applications. It does not create or upgrade the cluster. The registration policy accepts Kubernetes 1.35 and 1.36, including K3s version suffixes. Other versions fail registration with an explanation.

Live validation passed on K3s 1.35.5, K3s 1.36.2 and Kind Kubernetes 1.36.4. The K3s 1.35.5 to 1.36.2 upgrade also preserved the registered identities, accepted deployment metadata, Helm history and volume contents. These were disposable local Docker clusters, not production or managed-cloud clusters.

## Register a namespace

Create the namespace first. Supply its name, the selected kubeconfig context and either pasted credentials or a mounted kubeconfig. Dispatch requires HTTPS with certificate verification. Kubeconfigs that execute credential commands or load authentication plugins are rejected. Use embedded credentials and rotate them through the server update API.

Registration checks the version, required APIs, namespace availability and permissions. Dispatch records the `kube-system` namespace UID as cluster identity and the selected namespace UID as namespace identity. Credential updates must preserve both. Recreating a namespace requires a new target, even when its name is unchanged.

Grant `get` access to the named `kube-system` namespace and the target namespace. Within the target namespace, grant `get`, `list`, `watch`, `create`, `update`, `patch` and `delete` for:

- Core pods, services, secrets, configmaps and persistentvolumeclaims.
- Deployments, statefulsets and replicasets in `apps`.
- Jobs in `batch`.
- Ingresses in `networking.k8s.io`.

Also allow `get` on `pods/log` and creation of `SelfSubjectAccessReview` requests. Additional chart resources require their own permissions. Dispatch checks permissions through the Kubernetes authorization API. See [Kubernetes authorization](https://kubernetes.io/docs/reference/access-authn-authz/authorization/).

Supply a working ingress controller, DNS and TLS configuration for public routes. A storage class and provisioner must exist for charts that request persistent volumes. API discovery confirms resource APIs exist; it does not prove that an ingress controller or storage provisioner works.

For K3s, copy the intended context into a scoped kubeconfig rather than distributing the administrator credentials. See [K3s cluster access](https://docs.k3s.io/cluster-access).

## Ownership and recovery

Registered targets restrict Helm resources to their namespace. Charts may use supported built-in namespaced resources. Cluster resources, custom resources and removed API versions are rejected. Dispatch does not install CRDs, and chart hooks are disabled for these targets. Install cluster prerequisites through a separate administrator workflow.

Helm keeps ownership of chart resources. Dispatch does not adopt them through server-side apply. Release metadata retains the application, project, deployment, accepted specification digest and source commit. Helm replaces release descriptions after rollback or failed upgrades, so recovery also checks retained resource annotations and release labels. Existing release names must match Dispatch ownership before upgrade, rollback or deletion. Atomic upgrades also validate the older successful revision that Helm would select if the upgrade failed. Cancellation stops the candidate readiness check and waits for atomic cleanup before returning, so a fresh deployment cannot race a delayed rollback from the cancelled attempt. Cleanup failures remain deployment failures; cancellation does not guarantee that an unavailable cluster can restore the prior release. Older Dispatch metadata without a project ID must still match the globally unique application ID.

Before runtime access, Dispatch checks the recorded cluster and namespace identities again. A changed mounted kubeconfig cannot silently redirect deployment history to another target. The selected context supplies credentials; the controller's default context and `HELM_KUBE*` overrides are never fallbacks. Helm requests, including template `lookup` calls, cannot read another namespace or cluster-wide resource collections. Use separate namespaces and credentials for separate tenants. Stored kubeconfig and CA data remain encrypted and write-only through the API. Operations freeze the selected credentials in private temporary files and remove them afterward. Mounted kubeconfigs cannot change those files during an operation; unused contexts and their credentials are excluded.

Cleanup preserves protected storage. Removing a release does not grant permission to delete retained PVCs. Restoring a Helm release restores its retained chart inputs, not database contents or external side effects.

Existing targets without validation evidence retain their previous behavior until an operator saves and validates them. Managed OpenShift bootstrap remains a separate registration path.

## Validation limits

The shared runtime contract currently advertises Helm deploy and destroy. Existing diagnosis, logs and reviewed rollback APIs remain separate; they are not advertised as newly completed runtime operations.

TLS fixtures cover both accepted minor versions, selected-context isolation, unavailable APIs, permission failures and cancellation. Helm SDK fixtures cover repeat deployment and deletion, retained release ownership, namespace escape and removed APIs. SQLite checks preserve registration evidence across updates.

The live lifecycle test covers the shared deploy/destroy contract, readiness, pod logs, client recreation, retained rollback and PVC preservation. The cancellation cases first observe real partial objects, including an unready Pod, then cancel installation or upgrade. They check atomic cleanup, restoration of the prior healthy revision for upgrades, and deployment through a new client. Each case waits past the cancelled attempt's original readiness deadline and verifies that no late worker changes the replacement release or its history.

| Live target | Lifecycle and partial-apply recovery | Cluster upgrade |
| --- | --- | --- |
| K3s 1.35.5+k3s1 | Passed with the race detector | Starting version |
| K3s 1.36.2+k3s1 | Passed with the race detector | Passed from 1.35.5+k3s1 |
| Kind Kubernetes 1.36.4 | Passed with the race detector | Not exercised |

The upgrade test saves the original cluster and namespace UIDs, Deployment and PVC UIDs, accepted source commit and specification digest, Helm revision, and a unique value written to the volume. After replacing the disposable K3s container while retaining its data volume, the test uses the original registration evidence with a fresh client. It verifies those identities and records, upgrades the retained release, rolls back to its pre-cluster-upgrade revision, checks the volume contents and confirms release cleanup preserves the PVC.

### Reproduce the live checks

Install Docker, the Go version from `go.mod`, Python 3 and [Kind v0.33.0](https://github.com/kubernetes-sigs/kind/releases/tag/v0.33.0). The script pins the Kind and K3s images by digest. It creates privileged local Docker containers, pulls their images and uses private kubeconfig files. It requires a local Docker Unix socket and refuses remote Docker endpoints. It never reads the default kubeconfig or modifies host limits.

```sh
DISPATCH_TEST_KUBERNETES_ISOLATED=1 scripts/test-kubernetes-live.sh all
```

Use `kind` for the external-distribution lifecycle run or `upgrade` for both K3s versions and the cluster upgrade. Set `KIND=/absolute/path/to/kind` if the binary is outside `PATH`. Clusters run sequentially. The script checks the storage provisioner, removes its containers and data volume on exit, and reports cleanup failures as a failed run. Do not interrupt it with `SIGKILL`, which prevents shell cleanup. `scripts/test-kubernetes-live_test.sh` checks cleanup failure handling and remote-endpoint refusal with stubbed tools; those checks do not start a cluster.

For an already disposable cluster with a default storage provisioner, set `DISPATCH_TEST_KUBECONFIG` and `DISPATCH_TEST_KUBERNETES_ISOLATED=1`, then run:

```sh
go test -race ./internal/deploy -run '^TestKubernetesLifecycleIntegration$' -count=1 -v
```

The lifecycle test creates its own namespace and verifies the namespace UID before teardown. The separate upgrade test requires `DISPATCH_TEST_KUBERNETES_UPGRADE_PHASE=before` or `after` and a private `DISPATCH_TEST_KUBERNETES_UPGRADE_RECORD` path. The before phase intentionally retains its namespace. The after phase requires the same cluster upgraded from 1.35 to 1.36 and removes that namespace after validation. The script manages this sequence and deletes its data volume even if a phase fails.

These runs use Linux amd64, one node, administrator test credentials and local-path storage. They do not establish production availability during a control-plane upgrade, managed-cloud compatibility, multi-node recovery, OpenShift coverage, ingress/DNS/TLS behavior or application data migration safety. The initial Kind PVC run exposed local watcher exhaustion in kube-proxy; it passed after a completed disposable cluster was removed, without changing host settings. Check cluster prerequisites before attributing provisioner failures to Dispatch.

Dispatch does not perform the cluster upgrade for an operator. Follow the distribution's own procedure, such as [K3s manual upgrades](https://docs.k3s.io/upgrades/manual), and check chart APIs against the [Kubernetes API migration guide](https://kubernetes.io/docs/reference/using-api/deprecation-guide/).
