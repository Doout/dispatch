# Run a workflow from your laptop

Use the [CLI installer](../../README.md#install-the-doodship-cli) for the latest stable release or a specific version. To install manually, download the matching `doodship-{darwin,linux}-{arm64,amd64}.tar.gz` from [GitHub Releases](https://github.com/Doout/dispatch/releases) and check its digest against `SHA256SUMS`. Extract the `doodship` executable to a directory on your PATH.

To build the standalone Linux or macOS CLI from the repository root, use `make doodship`. Copy the resulting `doodship` executable onto your PATH. To install directly with Go, use `go install ./cmd/doodship`.

The examples deploy one ConfigMap. A shell job writes a message to the workflow output file, and Helm places that message in the ConfigMap. The slot has a fixed release name. The PR template inserts the PR number into the release name and message.

## Try the example

From the repository root, create a small local Git source. Doodship checks out committed files, so the example needs its own commit.

```sh
make doodship
runner="$PWD/doodship"
demo=$(mktemp -d)
cp -R examples/doodship/. "$demo/"
git -C "$demo" init -q -b main
git -C "$demo" add .
git -C "$demo" -c user.name=Example -c user.email=example@localhost commit -qm "Add hello workflow"
cd "$demo"

"$runner" workflow run --file slot.yaml --source hello=. --plan
"$runner" workflow run --file preview.yaml --pr 42 --source hello=. --plan
```

Use `--dry-run` or `--plan` to see pinned commits, target context, namespace, release and binding availability. It skips jobs, Helm and secret prompts. For an inventory from the YAML alone, use:

```sh
"$runner" workflow run --file slot.yaml --list-inputs
"$runner" workflow run --file preview.yaml --pr 42 --list-inputs
```

Each declared binding shows whether it is missing or supplied by the environment, an env file or `--var`. The report lists its consuming jobs and environment variable names, including finally jobs. Unavailable JSON keys are marked separately. `--list-inputs` also lists the generated and inherited environment variable names for each job. Values stay hidden. Arbitrary scripts may expect additional undeclared variables.

To generate a fillable file from a slot or PR workflow, add `--write-env-example` to `--list-inputs`:

```sh
doodship workflow run --file preview.yaml --pr 42 --list-inputs --write-env-example .env.example
cp .env.example .env.local
# Fill in .env.local, then check which bindings are available.
doodship workflow run --file preview.yaml --pr 42 --list-inputs --env-file .env.local
```

The example contains one blank assignment per declared secret reference, even when your environment already supplies it. Comments identify the consuming jobs and any JSON keys. Doodship creates a new file and preserves any existing file at that path. Pass the filled file to a normal run with the same `--env-file` option.

The full plan needs local checkouts. To deploy, use Helm 3.13+ and a kube context with access to the target namespace. These examples assume a local development cluster with context `kind-dev`. Substitute your own development context.

```sh
"$runner" workflow run --file slot.yaml --source hello=. --target local=kind-dev
"$runner" workflow run --file preview.yaml --pr 42 --source hello=. --target local=kind-dev
helm --kube-context kind-dev get manifest hello-preview-42 --namespace default
```

The preview ConfigMap contains `message: "Hello from PR 42"`. Rerunning either command upgrades its release. Remove the examples with `helm --kube-context kind-dev uninstall hello-slot hello-preview-42 --namespace default`.

## Use a slot or PR workflow

```sh
doodship workflow run --file deployment/slot1.yaml
doodship workflow run --file deployment/templates/preview.yaml --pr 142 --source service=../service-pr-142
```

- **Sources.** Doodship matches Git origins to the workflow's repository names. Keep clones together under one parent directory, named after the repositories, or supply repeated `--source alias=/path/to/clone` flags. Slots use each source's declared branch or ref, even when your working tree is on another branch. Git resolves local commits, tags and branches, including cached origin branches. Have all required commits, scripts and chart dependencies available locally before disconnecting.
- **PR revisions.** Supply a checkout at the intended PR commit for a watched alias. Doodship uses its committed HEAD. From a watched repository, your current checkout can supply the PR revision. Other sources use their configured refs. Supplying two watched checkouts links both HEADs. Doodship prints the SHAs so you can check what you are deploying. Uncommitted edits are excluded.
- **Targets.** A stage's `targetRef` names its kube context. If the laptop uses another name, pass `--target dev-005=my-context`. Doodship checks access before running build jobs. Slot runs install or upgrade the named Helm release. Preview runs label their releases. If a preview name is already owned elsewhere, supply a distinct instance ID, for example `--pr 142 --instance 142-local`.
- **Variables and secrets.** Bindings use their YAML `secretRef` names. Doodship reads `--var NAME=value` for ordinary values, then `--env-file FILE`, then the environment. An env file contains literal `NAME=value` lines, without shell expansion or quote processing. Keep secret values in the environment or a private file. On a terminal, missing values prompt once with input hidden. `--non-interactive` lists missing names and exits before running a job. Each job receives its own declared bindings and the local tool settings it needs.
- **Execution.** Jobs run trusted shell scripts as your laptop user and can use its Docker daemon, network and configured tools. Source revisions, dependency order, job concurrency, finally jobs, output bindings and Helm values follow the workflow file. Supply any tools the scripts themselves call. Images, registries and the destination cluster must be reachable when the scripts or Helm use them.
- **First-release scope.** Each invocation builds afresh. `reuse` and `sourcePaths` remain valid YAML, with no persistent build-result cache. Local deployments support automatic Helm stages. Stages with controller approvals, Pipeline checks or managed service bindings fail preflight. The CLI does not watch PRs or post GitHub reports. Use Helm to inspect or remove a local deployment.
