# Repository configuration

Dispatch loads Applications and Pipelines from YAML or JSON in a GitHub repository. Add the repository from **Applications > Add > GitHub configuration**. The default path is `.dispatch`.

Each sync reads every `.yaml`, `.yml`, and `.json` file below that path. The parser rejects unknown fields, invalid references, and duplicate resource names. A rejected sync does not replace the last valid configuration.

Imported resources start paused. Activate an Application to create its first immutable revision. Activate a Pipeline before an Application references it as a stage check.

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

Application jobs default to `reuse: onInputMatch`. The fingerprint includes the job definition, declared source revisions, and inputs. If only the service repository changes, `build-ui` reuses its latest successful result. Dispatch runs the job when there is no prior result, a declared output is missing, or an input changed. Set `reuse: never` to run a job every time. Pipeline and `finally` jobs always run.

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
