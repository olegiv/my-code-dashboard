# GitHub validation and the first application PR

The public repository starts empty. Establish `main` with a short README and the
unchanged GPL `LICENSE`, then put the complete application on
`initial-implementation` and open a normal PR into `main`. This makes every
application file part of the first PR's diff. A PR needs two existing branches;
GitHub cannot review a root commit against a nonexistent branch.
[GitHub PR documentation](https://docs.github.com/en/pull-requests/how-tos/create-pull-requests/creating-a-pull-request).

Preparing the following commits does **not** authorize creating either commit,
pushing either branch, opening the PR, enabling integrations or changing rules.
Keep local previews and reports in ignored `output/`, with real Git tree IDs;
there are no commit IDs until commits are approved and created.

## Two commit previews

1. **Bootstrap `main`:** only `README.md` and `LICENSE`. The README identifies the
   project and GPL-3.0-only license and says implementation will arrive by PR.
   Check its complete two-file diff, license identity, links and secret scan.
   Go tests, packaging and hosted checks are **not applicable** to this tree:
   it has no Go module or workflows. Do not describe them as passing.
2. **Application on `initial-implementation`:** the entire reviewed application
   tree, including its full README, instructions, tests and workflows. Preview
   the diff from the bootstrap **tree**, list every changed file, and provide
   the full commit message. Run `make commit-check`, `make pr-check`,
   `make package-check`, independent Codex review and a real Codex Security
   Standard scan on this candidate. Record direct verification and limitations.

Retain the application index while preparing previews: an alternate temporary
index can write the bootstrap tree without creating a commit, changing HEAD,
replacing working files or losing the application's staged selection. Neither
`git write-tree` nor a tree-to-tree diff creates a commit. Do not invent a parent
commit just to make local review tooling work.

After separate authorization, construct the two commits from their approved
trees/messages, with the bootstrap as the application's parent. Publish the
bootstrap on `main`, publish `initial-implementation`, then open the PR. Keep the
working application and private data intact throughout. Recheck both trees,
author identity, remote state and current approval before each side effect.
Do not use `--no-verify` or weaken the application's pre-push guard to accommodate
the documentation-only bootstrap. Its narrow, explicit scope requires its own
review; the full application must satisfy the normal gate before publication.

## Prepared Actions checks

All PR workflows use `pull_request`, never `pull_request_target`, and cover
`main` without path filters; Go checks also covers other PR base branches. They run on `main` pushes and support manual
dispatch, except dependency review, which needs a PR comparison.

| Workflow / job label | Required result for the application PR |
| --- | --- |
| Go checks / `test (ubuntu-latest)` | Build, uncached race tests, golangci-lint, vet, tidy/module verification, govulncheck and browser asset checksums |
| Go checks / `test (macos-latest)` | The same checks on macOS |
| Dependency review / `dependency-review` | Compare introduced dependencies with the bootstrap base |
| PR quality / `Workflow, shell and secret checks` | Complete public manifest, ShellCheck, actionlint v1.7.12 and Gitleaks v8.30.1 |
| PR quality / `Package verification` | Four target archives, corresponding source, SHA-256 checksums, metadata, notices and native Linux version smoke test |
| CodeQL / `CodeQL (go)` | Security-extended analysis with an explicit Go 1.26.9 build of every package |
| CodeQL / `CodeQL (javascript-typescript)` | Browser JavaScript analysis without a frontend build |
| CodeQL / `CodeQL (actions)` | Workflow security analysis |

CodeQL uses the pinned v4.39.0 action, with `security-events: write` limited to its
job. Other jobs have read-only repository access and no application credentials.
Checkouts do not retain Git credentials. There are no AI API keys in workflows.
GitHub supplies the JavaScript runtime used internally by Actions; developers and
the application still need no Node.js/npm installation.

Explicit CodeQL configuration is included in the application PR because the
bootstrap contains no Go code. Do not depend on automatic language discovery on
`main` to validate that first import. Analysis and SARIF processing must actually
finish; an accepted configuration is not a completed scan.
[Advanced CodeQL setup](https://docs.github.com/en/code-security/how-tos/find-and-fix-code-vulnerabilities/configure-code-scanning/configuring-advanced-setup-for-code-scanning),
[compiled-language builds](https://docs.github.com/en/code-security/how-tos/find-and-fix-code-vulnerabilities/manage-your-configuration/codeql-for-compiled-languages).

The PR package job does not upload archives or create tags/releases. The separate
manual **Prepare release archives** workflow verifies the same outputs and then
uploads an Actions artifact for seven days; in a public repository that artifact
is public to users with access to the run. It never creates a GitHub Release.
Manual dispatch requires that workflow to exist on the default branch, so the
PR's automatic package job provides the pre-merge coverage.
[GitHub workflow events](https://docs.github.com/en/actions/reference/workflows-and-actions/events-that-trigger-workflows).

## Hosted Codex reviews: configuration pending

After authorization, connect this repository in Codex and enable automatic
**Code Review** for all PRs. A manual `@codex review` request is also supported.
Verify the completed review for the final PR revision; a reaction alone is not
completion. Hosted comments have reporting thresholds, so they do not establish
that every file or every severity was exhaustively reviewed.
[Official Code Review setup](https://learn.chatgpt.com/docs/third-party/github).

**Codex Security Review** needs eligible workspace access and the connected
repository. Its hosted access has not been verified. After authorization, enable
automatic security review for all PRs and every push; choose Medium as the minimum
reported severity and inspect the full report, including lower-severity results.
Alternatively request `@codex security review`. Wait for the associated task and
Security Report to complete on the final revision: no posted findings can mean
that none met the threshold. Local Standard scans and hosted PR reviews are
separate checks. A PR containing all application code improves diff coverage but
does not guarantee exhaustive AI review. Do not replace unavailable access with a
successful placeholder job.
[Official Security Review setup and completion semantics](https://learn.chatgpt.com/docs/security/security-review).

## Merge policy to apply later

No settings are changed by these files. After the first authorized PR run,
record GitHub's actual check context names and configure a `main` ruleset to
require the checks above, a PR before merge, and an up-to-date branch. Require
completed Codex and Codex Security reports for the final revision as a documented
maintainer gate. Add them as required status checks only if their integration
actually exposes stable, enforceable contexts; comments are not status checks.

Until that policy is authorized and enabled, successful Actions are advisory and
do not prevent bypassing merge checks. Hosted check results, CodeQL execution,
Linux runtime testing, integration permissions and branch-rule enforcement stay
pending until exercised on GitHub. Local tests cannot claim those results.
