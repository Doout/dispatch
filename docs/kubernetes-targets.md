# Kubernetes and K3s targets

Dispatch registers an existing cluster namespace for Helm applications. It does not create or upgrade the cluster. The registration policy accepts Kubernetes 1.35 and 1.36, including K3s version suffixes. Other versions fail registration with an explanation.

The local K3s 1.36.2 lifecycle run passed the shared deploy/destroy contract, readiness, pod logs, client restart, retained rollback and PVC preservation. Kubernetes 1.35 currently has TLS API fixture coverage. Cross-version upgrades and external-cluster runs remain required for issues #20 and #21.

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

Helm keeps ownership of chart resources. Dispatch does not adopt them through server-side apply. Release metadata retains the application, project, deployment, accepted specification digest and source commit. Helm replaces release descriptions after rollback or failed upgrades, so recovery also checks retained resource annotations and release labels. Existing release names must match Dispatch ownership before upgrade, rollback or deletion. Atomic upgrades also validate the older successful revision that Helm would select if the upgrade failed. Older Dispatch metadata without a project ID must still match the globally unique application ID.

Before runtime access, Dispatch checks the recorded cluster and namespace identities again. A changed mounted kubeconfig cannot silently redirect deployment history to another target. The selected context supplies credentials; the controller's default context and `HELM_KUBE*` overrides are never fallbacks. Helm requests, including template `lookup` calls, cannot read another namespace or cluster-wide resource collections. Use separate namespaces and credentials for separate tenants. Stored kubeconfig and CA data remain encrypted and write-only through the API. Operations freeze the selected credentials in private temporary files and remove them afterward. Mounted kubeconfigs cannot change those files during an operation; unused contexts and their credentials are excluded.

Cleanup preserves protected storage. Removing a release does not grant permission to delete retained PVCs. Restoring a Helm release restores its retained chart inputs, not database contents or external side effects.

Existing targets without validation evidence retain their previous behavior until an operator saves and validates them. Managed OpenShift bootstrap remains a separate registration path.

## Validation limits

The shared runtime contract currently advertises Helm deploy and destroy. Existing diagnosis, logs and reviewed rollback APIs remain separate; they are not advertised as newly completed runtime operations.

TLS fixtures cover both accepted minor versions, selected-context isolation, unavailable APIs, permission failures and cancellation. Helm SDK fixtures cover repeat deployment and deletion, retained release ownership, namespace escape and removed APIs. SQLite checks preserve registration evidence across updates.

The opt-in test requires `DISPATCH_TEST_KUBECONFIG` and `DISPATCH_TEST_KUBERNETES_ISOLATED=1`. Run `go test -race ./internal/deploy -run TestKubernetesLifecycleIntegration -count=1` against a disposable cluster with a default storage provisioner. The test creates a namespace, verifies its UID before teardown and removes its own resources.

Before closing #20 or #21, add live mid-operation cancellation and partial-apply recovery, then repeat the lifecycle cases on Kubernetes 1.35 and an external cluster. Exercise a supported cluster upgrade while preserving deployment history. Check chart APIs against the [Kubernetes API migration guide](https://kubernetes.io/docs/reference/using-api/deprecation-guide/).
