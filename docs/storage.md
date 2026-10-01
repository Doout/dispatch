# Storage ownership and deletion

Open a deployment target's **Storage** action to view recorded volumes, their
application or service owner, consumers, inspection state and data policy. A
controller owner can choose **Inspect target storage** to refresh the inventory.
Project members see only storage owned by projects they can view. Unverified
storage is visible only to controller owners and cannot be adopted through a
client-supplied ownership claim.
Only controller owners see consumer names and mount paths, because a shared
volume can have consumers in other projects. Other viewers see consumer counts
and activity.

The default policy is **Retain data**. Removing an application, preview or service
registration does not grant permission to remove its data. Storage records survive
workload deletion and preserve the original project, owner and runtime identity.
An orphan is retained storage whose original workload no longer exists.

To intentionally remove data, remove its runtime consumers, change its policy to
**Allow reviewed deletion**, then choose **Delete data** and confirm the reviewed
name. The server checks ownership, runtime identity, consumers and current policy
again before deletion. A changed owner, policy, consumer or replaced volume
invalidates the review. Changing a volume's policy alone does not delete anything.
Deletion records the actor, resource, confirmed policy/version and outcome in audit
history. Deleted storage remains in the inventory as absent.

An inaccessible target never proves that its data is absent. A failed or
interrupted deletion remains inspectable; refresh the target and review again
before retrying. Docker deletion does not use force. Kubernetes deletion uses the
PVC UID as a precondition and reports pending finalizers as unfinished work.

## Supported runtimes

Local Docker targets and targets bound to an enrolled Docker agent inspect named
and anonymous volumes, provenance labels and every container's volume mounts,
including stopped containers. Enrolled targets use typed `storage_inspect` and
`storage_delete` jobs; they never inspect or delete data on the controller daemon.
The agent rechecks volume identity, provenance and consumers immediately before
removal. Only ownership labels are returned; arbitrary volume labels are excluded.
Dispatch adopts existing volumes only when runtime labels identify an application
or built-in service provisioning run on the same target and in the correct
project. Unlabeled volumes remain unverified and protected. Bind-mounted host
directories are not claimed or deleted by this workflow.

Kubernetes/OpenShift targets inspect PVCs, pods and standard workload references.
The credential needs list access to PVCs, pods, Deployments, StatefulSets,
DaemonSets, ReplicaSets, Jobs and CronJobs in the target's default namespace,
configured application/service namespaces and namespaces of retained inventory.
Dispatch queries each namespace separately. Missing permission blocks inspection
and destructive cleanup. PVC ownership uses Dispatch labels or a verified Helm
release identity; arbitrary names alone do not establish ownership. PVC owner
references and workload references remain consumers even between running pods.

New Helm deployments retain PVCs/PVs in their saved manifests and set StatefulSet
claim retention to `Retain`. Explicit data deletion remains a separate storage
operation. Legacy releases whose manifests would delete PVCs/PVs, a namespace or
StatefulSet data must be upgraded to retain those resources before cleanup.
Uninstall hooks are disabled because arbitrary hook code cannot honor these data
protection rules. Preview namespace cleanup stops while any PVC or recorded
dependent storage remains in that namespace.

A target cannot be removed while recorded storage still depends on it. Provider
disk records use the same guard: keeping a database record does not preserve a
disk that disappears with its machine. A provider must establish independent
storage preservation before allowing server deletion. Actual provider disk
creation/deletion, remote Kubernetes storage, backups and snapshots are separate
capabilities; unsupported storage deletion fails before mutation.

The controller currently has one active execution process. Storage inspection,
policy changes and Dispatch runtime mutations serialize per target. External
administrators can still change a runtime directly; re-inspection, object identity
checks and runtime in-use protections reject detected conflicts.

## API

| Endpoint | Behavior |
| --- | --- |
| `GET /api/v1/storage?serverId=&projectId=` | Project-filtered recorded inventory; does not contact the runtime. |
| `GET /api/v1/storage/{id}` | Visible storage record and its current revision. |
| `POST /api/v1/servers/{id}/storage/reconcile` | Controller-owner runtime inspection and verified adoption. |
| `PUT /api/v1/storage/{id}/policy` | Project configuration permission; accepts `revision` and `policy` of `retain` or `destroy`. |
| `POST /api/v1/storage/{id}/delete-preview` | Current data-deletion consequences, blocked reason and confirmation version. |
| `DELETE /api/v1/storage/{id}` | Requires the common destructive confirmation returned by review. |

Policies and ownership are stored in both SQLite and PostgreSQL by migration
`070_storage_ownership`; migration `080_remote_storage` permits target inspection
while an uncertain storage mutation remains protected. No runtime credentials or arbitrary label values are
stored in the inventory. Backup and restore workflows can reference these stable
storage records without changing their deletion policy.

An unknown remote deletion blocks further deletion of that storage record. After
its execution lease expires, inspect the target again. A successful inventory
started after the uncertain outcome and old lease reconciles the operation while
preserving its receipt. Any retry still requires a new data-deletion review with
current ownership, retention policy and consumers. Inspection failure cannot
unlock the operation or declare data absent.
