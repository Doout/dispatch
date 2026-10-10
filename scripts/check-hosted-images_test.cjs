#!/usr/bin/env node
const assert = require('node:assert/strict');
const { readFileSync } = require('node:fs');
const { join } = require('node:path');
const test = require('node:test');
const verify = require('./hosted-image-source.cjs');
const checkImage = require('./check-hosted-image.cjs');

const sha = 'a'.repeat(40);
function fixture() {
  const run = {
    id: 123, workflow_id: 456, status: 'completed', conclusion: 'success',
    event: 'push', head_branch: 'main', head_sha: sha,
    head_repository: { full_name: 'Doout/dispatch' },
  };
  const context = {
    eventName: 'workflow_run', repo: { owner: 'Doout', repo: 'dispatch' },
    payload: { workflow_run: { id: run.id, head_sha: sha } },
  };
  const workflow = { path: '.github/workflows/ci.yml' };
  const branch = { commit: { sha } };
  const outputs = {};
  const github = { rest: {
    actions: {
      getWorkflowRun: async (input) => { assert.equal(input.run_id, run.id); return { data: run }; },
      getWorkflow: async (input) => { assert.equal(input.workflow_id, run.workflow_id); return { data: workflow }; },
    },
    repos: { getBranch: async (input) => { assert.equal(input.branch, 'main'); return { data: branch }; } },
  } };
  const core = { setOutput: (key, value) => { outputs[key] = value; }, notice: () => {} };
  return { github, context, core, run, workflow, branch, outputs };
}

test('successful main CI publishes its exact tested commit', async () => {
  const f = fixture();
  await verify(f);
  assert.deepEqual(f.outputs, { eligible: 'true', sha });
});

for (const [name, modify] of Object.entries({
  'failed CI': f => { f.run.conclusion = 'failure'; },
  'cancelled CI': f => { f.run.conclusion = 'cancelled'; },
  'skipped CI': f => { f.run.conclusion = 'skipped'; },
  'CI being rerun': f => { f.run.status = 'in_progress'; },
  'pull request run': f => { f.run.event = 'pull_request'; },
  'manually dispatched CI': f => { f.run.event = 'workflow_dispatch'; },
  'other branch': f => { f.run.head_branch = 'feature'; },
  'fork named main': f => { f.run.head_repository.full_name = 'someone/dispatch'; },
  'other workflow named CI': f => { f.workflow.path = '.github/workflows/another.yml'; },
  'main advanced during build': f => { f.branch.commit.sha = 'b'.repeat(40); },
  'event and API revision mismatch': f => { f.context.payload.workflow_run.head_sha = 'b'.repeat(40); },
  'invalid revision': f => { f.run.head_sha = 'main'; },
})) {
  test(`rejects ${name}`, async () => {
    const f = fixture();
    modify(f);
    await verify(f);
    assert.deepEqual(f.outputs, { eligible: 'false' });
  });
}

test('rejects invocation from another repository or trigger', async () => {
  for (const modify of [
    f => { f.context.repo.owner = 'someone'; },
    f => { f.context.eventName = 'pull_request'; },
    f => { delete f.context.payload.workflow_run; },
  ]) {
    const f = fixture();
    modify(f);
    await assert.rejects(verify(f));
    assert.deepEqual(f.outputs, {});
  }
});

test('API failures do not authorize publishing', async () => {
  const f = fixture();
  f.github.rest.repos.getBranch = async () => { throw new Error('unavailable'); };
  await assert.rejects(verify(f));
  assert.deepEqual(f.outputs, {});
});

test('workflow builds the checked revision and advances main only after rechecking it', () => {
  const workflow = readFileSync(join(__dirname, '../.github/workflows/hosted-images.yml'), 'utf8');
  assert.match(workflow, /workflows: \[CI\]/);
  assert.match(workflow, /github\.event\.workflow_run\.event == 'push'/);
  assert.match(workflow, /github\.event\.workflow_run\.head_repository\.full_name == 'Doout\/dispatch'/);
  assert.match(workflow, /ref: \$\{\{ needs\.source\.outputs\.sha \}\}/);
  assert.match(workflow, /file: Containerfile\.hosted/);
  assert.match(workflow, /platforms: linux\/amd64,linux\/arm64/);
  assert.match(workflow, /if: steps\.existing\.outputs\.digest == ''/);
  assert.match(workflow, /if: steps\.current\.outputs\.eligible == 'true'/);
  assert.match(workflow, /run: node scripts\/check-hosted-image\.cjs/);
  assert.equal(workflow.match(/require\('\.\/scripts\/hosted-image-source\.cjs'\)/g).length, 2);
  assert.ok(workflow.indexOf('Check both architectures') < workflow.indexOf('Recheck main before updating its tag'));
  assert.ok(workflow.indexOf('Recheck main before updating its tag') < workflow.indexOf('name: Update the main image'));
  assert.doesNotMatch(workflow, /download-artifact|workflow_dispatch|pull_request_target|\bssh\b/);
});

function imageFixture() {
  const config = { image: 'ghcr.io/doout/dispatch-platform', digest: `sha256:${'a'.repeat(64)}`, sha };
  const manifest = { manifests: [
    { digest: `sha256:${'b'.repeat(64)}`, platform: { os: 'linux', architecture: 'amd64' } },
    { digest: `sha256:${'c'.repeat(64)}`, platform: { os: 'linux', architecture: 'arm64' } },
    { digest: `sha256:${'d'.repeat(64)}`, platform: { os: 'unknown', architecture: 'unknown' } },
  ] };
  const calls = [];
  const docker = args => {
    calls.push(args);
    if (args[0] === 'buildx') return JSON.stringify(manifest);
    return `Dispatch platform main-${sha}\n`;
  };
  return { config, manifest, calls, docker };
}

test('checks each architecture by its child digest with runtime restrictions', () => {
  const f = imageFixture();
  f.manifest.manifests.push({
    digest: `sha256:${'e'.repeat(64)}`, platform: { os: 'linux', architecture: 'amd64' },
    annotations: { 'vnd.docker.reference.type': 'attestation-manifest' },
  });
  checkImage(f.config, f.docker);
  assert.deepEqual(f.calls[0], ['buildx', 'imagetools', 'inspect', '--raw', `${f.config.image}@${f.config.digest}`]);
  assert.equal(f.calls.length, 3);
  for (const [index, architecture] of ['amd64', 'arm64'].entries()) {
    assert.deepEqual(f.calls[index + 1], ['run', '--rm', '--platform', `linux/${architecture}`,
      '--network', 'none', '--read-only', '--cap-drop', 'ALL', '--security-opt', 'no-new-privileges',
      `${f.config.image}@${f.manifest.manifests[index].digest}`, 'version']);
  }
});

for (const [name, modify] of Object.entries({
  'missing arm64': f => { f.manifest.manifests.splice(1, 1); },
  'wrong operating system': f => { f.manifest.manifests[1].platform.os = 'windows'; },
  'duplicate amd64': f => { f.manifest.manifests.push(f.manifest.manifests[0]); },
  'invalid child digest': f => { f.manifest.manifests[1].digest = 'latest'; },
  'single platform manifest': f => { delete f.manifest.manifests; },
})) {
  test(`does not execute images with ${name}`, () => {
    const f = imageFixture();
    modify(f);
    assert.throws(() => checkImage(f.config, f.docker));
    assert.equal(f.calls.length, 1);
  });
}

for (const [index, architecture] of ['amd64', 'arm64'].entries()) {
  test(`rejects an unexpected ${architecture} image version and stops checking`, () => {
    const f = imageFixture();
    const docker = args => {
      const version = f.docker(args);
      return args.includes(`linux/${architecture}`) ? 'Dispatch platform development\n' : version;
    };
    assert.throws(() => checkImage(f.config, docker), /unexpected version/);
    assert.equal(f.calls.length, index + 2);
  });
}

test('Docker pull and runtime failures fail the check', () => {
  const f = imageFixture();
  const failure = new Error('Docker failed');
  assert.throws(() => checkImage(f.config, () => { throw failure; }), error => error === failure);
  assert.throws(() => checkImage(f.config, args => {
    if (args[0] === 'buildx') return f.docker(args);
    throw failure;
  }), error => error === failure);
});
