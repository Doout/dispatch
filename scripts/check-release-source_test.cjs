const assert = require('node:assert/strict');
const test = require('node:test');
const verify = require('./release-source.cjs');

const sha = 'a'.repeat(40);
function fixture() {
  process.env.TESTED_SHA = sha;
  const run = { id: 123, workflow_id: 456, head_sha: sha, event: 'push', head_branch: 'main',
    head_repository: { full_name: 'Doout/dispatch' }, status: 'completed', conclusion: 'success' };
  const runs = [run], outputs = {}, workflow = { path: '.github/workflows/ci.yml' };
  return { run, runs, outputs, workflow, context: { repo: { owner: 'Doout', repo: 'dispatch' } },
    github: { rest: { actions: {
      listWorkflowRuns: async input => {
        assert.equal(input.head_sha, sha);
        assert.equal(input.workflow_id, 'ci.yml');
        return { data: { workflow_runs: runs } };
      },
      getWorkflow: async () => ({ data: workflow }),
    } } }, core: { setOutput: (key, value) => { outputs[key] = value; }, notice: () => {} } };
}

test('reuses only successful CI for the exact main revision', async () => {
  const f = fixture();
  await verify(f);
  assert.deepEqual(f.outputs, { 'run-id': '123' });
});

for (const [name, modify] of Object.entries({
  failure: f => { f.run.conclusion = 'failure'; },
  cancelled: f => { f.run.conclusion = 'cancelled'; },
  skipped: f => { f.run.conclusion = 'skipped'; },
  running: f => { f.run.status = 'in_progress'; },
  'another revision': f => { f.run.head_sha = 'b'.repeat(40); },
  'pull request': f => { f.run.event = 'pull_request'; },
  'another branch': f => { f.run.head_branch = 'feature'; },
  fork: f => { f.run.head_repository.full_name = 'someone/dispatch'; },
  'another workflow': f => { f.workflow.path = '.github/workflows/other.yml'; },
  'missing CI': f => { f.runs.length = 0; },
  'invalid revision': () => { process.env.TESTED_SHA = 'main'; },
  'newer failed run': f => { f.runs.push({ ...f.run, id: 124, conclusion: 'failure' }); },
})) {
  test(`refuses publishing with ${name}`, async () => {
    const f = fixture(); modify(f);
    await assert.rejects(verify(f));
    assert.deepEqual(f.outputs, {});
  });
}

test('API errors stop publication', async () => {
  const f = fixture();
  f.github.rest.actions.listWorkflowRuns = async () => { throw new Error('API unavailable'); };
  await assert.rejects(verify(f), /API unavailable/);
});
