// Release only an exact revision that completed the main-branch CI workflow.
module.exports = async function releaseSource({ github, context, core }) {
  const sha = process.env.TESTED_SHA;
  if (context.repo.owner !== 'Doout' || context.repo.repo !== 'dispatch' || !/^[a-f0-9]{40}$/.test(sha || '')) {
    throw new Error('Invalid release repository or revision');
  }
  const { data } = await github.rest.actions.listWorkflowRuns({
    ...context.repo, workflow_id: 'ci.yml', branch: 'main', event: 'push', head_sha: sha, per_page: 100,
  });
  const runs = data.workflow_runs.filter(run => run.head_sha === sha && run.event === 'push' &&
    run.head_branch === 'main' && run.head_repository?.full_name === 'Doout/dispatch');
  runs.sort((a, b) => b.id - a.id);
  const run = runs[0];
  if (!run || run.status !== 'completed' || run.conclusion !== 'success') {
    throw new Error(`Release requires successful main CI for ${sha}. Finish CI before tagging this commit.`);
  }
  const { data: workflow } = await github.rest.actions.getWorkflow({
    ...context.repo, workflow_id: run.workflow_id,
  });
  if (workflow.path !== '.github/workflows/ci.yml') {
    throw new Error('Release validation came from an unexpected workflow');
  }
  core.setOutput('run-id', String(run.id));
  core.notice(`Reusing completed validation: ${run.html_url}`);
};
