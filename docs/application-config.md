# Repository configuration

Dispatch loads Applications and Pipelines from YAML or JSON in a GitHub repository. Add the repository from **Applications > Add > GitHub configuration**. The default path is `.dispatch`.

Each sync reads every `.yaml`, `.yml`, and `.json` file below that path. The parser rejects unknown fields, invalid references, and duplicate resource names. A rejected sync does not replace the last valid configuration.

Imported Applications start their first deployment automatically when an active configuration syncs. Subsequent source or application configuration changes create a new immutable revision. Pipelines are available for stage checks immediately. Pause an Application to stop automatic deployments; syncing preserves that choice.

## Application

```yaml
apiVersion: dispatch/v1alpha1
kind: Application
metadata:
  name: storefront
spec:
  sources:
    service:
      repository: example/storefront-api
      branch: main
    ui:
      repository: example/storefront-web
      branch: main
    chart:
      repository: example/deployment-config
      branch: main
      path: charts/storefront

  jobs:
    build-service:
      runFrom: service
      run: ./scripts/build-image.sh
      secrets:
        REGISTRY_HOST:
          secretRef: registry-host
        REGISTRY_USERNAME:
          secretRef: registry-username
        REGISTRY_PASSWORD:
          secretRef: registry-password
      outputs: [imageRepository, imageTag]

    build-ui:
      runFrom: chart
      sources: [ui]
      run: ./scripts/build-ui.sh "{{ sources.ui.path }}"
      secrets:
        REGISTRY_HOST:
          secretRef: registry-host
        REGISTRY_USERNAME:
          secretRef: registry-username
        REGISTRY_PASSWORD:
          secretRef: registry-password
      outputs: [imageRepository, imageTag]

  deployments:
    storefront:
      helm:
        sourceRef: chart
        namespace: storefront-dev
        releaseName: storefront
        values:
          environment: development
        bindings:
          backend.image.repository:
            outputRef: build-service.imageRepository
          backend.image.tag:
            outputRef: build-service.imageTag
          ui.image.repository:
            outputRef: build-ui.imageRepository
          ui.image.tag:
            outputRef: build-ui.imageTag

  stages:
    - name: development
      targetRef: development
      deploy: [storefront]
      url: https://storefront-dev.example.com
      checks:
        e2e:
          pipelineRef: storefront-e2e
          with:
            base-url: "{{ stage.url }}"

    - name: staging
      targetRef: staging
      deploy: [storefront]
      url: https://storefront-staging.example.com
      checks:
        e2e:
          pipelineRef: storefront-e2e
          with:
            base-url: "{{ stage.url }}"

    - name: production
      targetRef: production
      deploy: [storefront]
      approval: required

  finally:
    publish-result:
      runFrom: chart
      run: ./scripts/publish-result.sh
```

`runFrom` selects the repository that contains the script. `sources` adds repositories that the command reads. Runtime templates expose `path`, `commit`, and `branch` for each declared source.

Application jobs default to `reuse: onInputMatch`. Matching jobs share successful results across Applications in the same repository configuration. Concurrent requests for identical inputs wait for the first build. This coordination works within one controller; it does not coordinate multiple controller replicas.

The fingerprint includes the job definition, declared source revisions, inputs, resolved secrets, and controller platform. Changing a secret invalidates the result. Separate configuration sources do not share results.

If only the service repository changes, `build-ui` reuses its latest successful result. Dispatch runs the job when there is no prior result, a declared output is missing, or an input changed. Pipeline and `finally` jobs always run.

A job receives source variables only for its `runFrom` and `sources` declarations. Reusable jobs must produce outputs from those declared inputs. Set `reuse: never` for jobs that depend on per-run IDs, time, or external mutable state.

Jobs receive `DISPATCH_OUTPUT_FILE` and `GITHUB_OUTPUT`. Write either JSON:

```json
{"imageRepository":"registry.example.com/storefront/api","imageTag":"sha-123"}
```

or dotenv:

```text
imageRepository=registry.example.com/storefront/api
imageTag=sha-123
```

Dispatch keeps only names declared under `outputs`. It resolves secrets when the job starts and passes them only to that process.

Every run snapshots the exact commit for every source. Stages deploy that same revision in order. A failed deployment or check stops promotion. `approval: required` pauses before the stage deploys.

## Pipeline

```yaml
apiVersion: dispatch/v1alpha1
kind: Pipeline
metadata:
  name: storefront-e2e
spec:
  inputs:
    base-url:
      required: true
  sources:
    tests:
      repository: example/storefront-tests
      branch: main
  jobs:
    test:
      runFrom: tests
      run: ./scripts/e2e.sh "{{ inputs.base-url }}"
  finally:
    upload-results:
      runFrom: tests
      run: ./scripts/upload-results.sh
```

A stage check starts a named active Pipeline and waits for it. Pipeline `finally` jobs run after the main jobs, including after a failure.

## Event delivery

The default update mode uses webhooks and polling. Dispatch processes `push` deliveries and compares the configuration branch and each referenced source branch on a schedule. Polling still detects a change when the controller is private or GitHub misses a webhook delivery.

The GitHub App registration must subscribe to `push` and grant read access to repository contents. Commit status publishing also needs write access to commit statuses. The Dispatch manifest requests both settings. Update existing registrations in GitHub if they lack either permission.

Polling checks each imported source at its configured interval. The minimum interval is 30 seconds. A configuration may use only webhooks or only polling.

## Shared charts and values files

A source can use `ref` instead of `branch` to select a full commit SHA, tag or
branch. Commits pin the baseline across runs. Branches and tags resolve to an
exact commit when a run starts. Retries and later stages use that recorded commit.

Use `chartPath` when the chart is below the source directory. Existing Applications
can keep using their source's `path` with no `chartPath`.

```yaml
sources:
  platform:
    repository: example/platform
    ref: chart-test-branch
  overrides:
    repository: example/deployment-config
    branch: main

deployments:
  app:
    helm:
      sourceRef: platform
      chartPath: charts/app
      valuesFiles:
        - sourceRef: platform
          path: values/default.yaml
        - sourceRef: platform
          path: values/development.yaml
        - sourceRef: overrides
          path: slots/engineer-1.yaml
      values:
        replicas: 1
```

Paths are relative to the selected source directory and must stay inside it.
The controller reads files at the run's source commits, in the listed order.
Helm merges maps and replaces lists. Later files override earlier files, inline
`values` override files, and output `bindings` apply last. Each deployment log
records file paths, commits and content hashes, without printing file contents.

File contents go to Helm without Pipeline expression expansion. Helm expressions
such as `{{ .Values.name }}` remain intact. Existing inline values still support
Pipeline expressions. Upgrades reset old release values before applying the new
inputs, matching `helm upgrade --reset-values`.

Keep chart experiments on a branch of the chart repository. Test that branch in
one engineer slot, then open a PR with those commits into the shared baseline.
Keep slot identities and experiment-only overrides in the configuration repository.
After merging a shared default, remove any temporary override that would mask it.

## Inspecting chart values

The deployment's Chart values view shows chart defaults, schema fields and values
referenced by templates or helpers. It follows selected image keys and references
inside Helm value expressions. Unresolved dynamic lookups retain their input
section and show a note. This analysis affects the display only.

Use Show all supplied values to inspect the complete merged input. New managed
deployments also record each value's source file, inline override or build output.
Older deployments show Supplied values when that source was not recorded.

The view reads the chart stored with the current Helm release. If the selected
deployment no longer matches that release, or the cluster cannot be read, it shows
the saved supplied values with an explanation. Secret redaction applies to both
views.

## Generate Applications from slot values

An ApplicationTemplate replaces repeated Application documents. The Pipeline
discovers files in the same repository and commit as the template, then expands
one Application per matching file. The generated documents stay in the database;
there are no generated files to commit.

```yaml
apiVersion: dispatch/v1alpha1
kind: ApplicationTemplate
metadata:
  name: service-slots
spec:
  files:
    path: helm-values/slots
    pattern: slot*.yaml
  parameters:
    chartRef: main
    target: development
  template:
    apiVersion: dispatch/v1alpha1
    kind: Application
    metadata:
      name: ${slot.name}
    spec:
      sources:
        chart:
          repository: platform/charts
          ref: ${param.chartRef}
        slot-config:
          repository: team/deployments
          ref: ${config.revision}
      deployments:
        service:
          helm:
            sourceRef: chart
            chartPath: charts/service
            releaseName: ${slot.name}
            valuesFiles:
              - sourceRef: slot-config
                path: helm-values/common.yaml
              - sourceRef: slot-config
                path: ${slot.path}
      stages:
        - name: development
          targetRef: ${param.target}
          deploy: [service]
```

Here, `team/deployments` is the repository containing the template and slot
files. Use its actual repository name. Pinning `slot-config` to
`${config.revision}` keeps discovery and deployment on the same commit.

A slot file contains ordinary Helm values and optional Pipeline parameters:

```yaml
_pipeline:
  chartRef: my-chart-experiment
replicas: 1
```

Only parameters declared in the template may be overridden, and their values
must be strings. The Pipeline strips the reserved top-level `_pipeline` map
before passing a values file to Helm. Common settings belong in a shared values
file loaded before the slot file.

`${slot.name}` is the filename without its extension. `${slot.path}` is its
repository-relative path. `${config.revision}` is the resolved configuration
commit. `${param.NAME}` selects a declared parameter. Substitution happens in
YAML scalar nodes and keys; Helm and job expressions are preserved.

Discovery uses a repository-relative directory and a filename glob, without
recursive glob patterns. Subdirectories are ignored. Ordinary Applications and
Pipelines may coexist with templates, but generated names must remain unique.
Discovery is limited to 64 configuration files per directory read and 256
generated resources per sync. Invalid files retain the previous valid resource
set.

Every generated Application has independent Run, Pause and history controls.
New Applications activate automatically, consistent with ordinary repository
Applications. Existing IDs and pause state survive moving a definition to a
template. Removing a slot file stops future runs; it does not uninstall its
release or cancel an in-flight deployment. Readding a removed slot requires
activation.

Applications sharing the configuration repository still observe its commits.
Editing one slot file can therefore trigger other active slots whose sources
include that repository.

Deploy a Pipeline version with ApplicationTemplate support before changing a
watched repository to this layout. Pause affected Applications during migration,
sync the new template and verify their IDs before resuming them.
