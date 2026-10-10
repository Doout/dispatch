// Verify the source before publishing a hosted image or advancing its main tag.
module.exports = async function hostedImageSource({ github, context, core }) {
  const repo = `${context.repo.owner}/${context.repo.repo}`;
  const eventRun = context.payload.workflow_run;
  if (repo !== 'Doout/dispatch' || context.eventName !== 'workflow_run' ||
      !Number.isSafeInteger(eventRun?.id)) {
    throw new Error('Hosted images require a CI workflow completion in Doout/dispatch.');
  }
  const { data: run } = await github.rest.actions.getWorkflowRun({ ...context.repo, run_id: eventRun.id });
  const { data: workflow } = await github.rest.actions.getWorkflow({ ...context.repo, workflow_id: run.workflow_id });
  const { data: branch } = await github.rest.repos.getBranch({ ...context.repo, branch: 'main' });
  const eligible = run.status === 'completed' && run.conclusion === 'success' &&
    run.event === 'push' && run.head_branch === 'main' &&
    run.head_repository?.full_name === repo && workflow.path === '.github/workflows/ci.yml' &&
    /^[a-f0-9]{40}$/.test(run.head_sha) && run.head_sha === eventRun.head_sha &&
    branch.commit.sha === run.head_sha;
  core.setOutput('eligible', String(eligible));
  if (eligible) {
    core.setOutput('sha', run.head_sha);
  } else {
    core.notice('Skipping publication: this run is not successful CI for the current main commit.');
  }
};
