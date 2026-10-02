# Repository access and recovery

GitHub configuration sources save the repository's numeric ID as well as its
name. Before a sync or a new workflow resolves sources, Dispatch checks the
App's current repository catalog. A renamed, archived, disabled, or inaccessible
configuration repository blocks that work. Existing deployments and accepted
workflow revisions keep their saved configuration and commit records.

Existing sources acquire an ID on their first successful sync after upgrading.
Sources using a saved SSH key or token continue to use their exact clone URL.
GitHub identity checks apply to the configuration repository. They do not pin
the identities of every additional source declared by an Application.

## Recover a source

Edit the configuration source and select **Check repository access**. The result
shows what Dispatch observed and how to recover. Checking access does not sync
configuration or deploy an application.

| Result | Next step |
| --- | --- |
| Renamed | Review the new name, select **Use owner/name**, and save. |
| Archived | Unarchive the repository in GitHub or select a replacement. |
| Disabled | Resolve GitHub's repository restriction before syncing. |
| Inaccessible | Restore App installation access and required permissions, then check again. |
| Deleted | Restore the repository in GitHub or select a replacement. |
| Identity changed | The saved name now belongs to a different repository. Select that replacement explicitly before saving. |
| Unavailable | Check the GitHub connection and retry. |

The repository picker shows IDs and archived or disabled state. Loading branches
never replaces the branch you entered, even when loading fails or the repository
has no branches. A repository with more than 1,000 branches requires entering the
exact branch name. Saving checks current identity and access again.

After access returns, sync to validate the configuration. Dispatch retains the
last accepted configuration if that sync fails. A successful sync resumes the
source's existing automatic deployment behavior.

## Access and evidence

Owners can list repositories for the selected GitHub App. Project members with
`project.configure` can check their source and list its repository and branches.
These source routes do not expose the App's other repositories or credential IDs.

GitHub can return 404 for a private repository when authentication is missing or
insufficient. Dispatch reports missing access as inaccessible, not deleted.
See [GitHub's API troubleshooting guide](https://docs.github.com/en/rest/using-the-rest-api/troubleshooting-the-rest-api).

The deleted state requires saved evidence for the same App and repository ID
from a verified `repository.deleted` delivery. A fresh catalog entry with that ID
takes precedence when the repository is restored. The delivery worker must
verify the App signature and match an existing repository binding before writing
that evidence. See [GitHub's repository webhook](https://docs.github.com/en/webhooks/webhook-events-and-payloads#repository).

The [OpenAPI contract](openapi.yaml) documents the repository catalog, branch
lookup, access check, and explicit replacement fields.
