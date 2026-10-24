# Public repository and initial publication

Repository: [olegiv/my-code-dashboard](https://github.com/olegiv/my-code-dashboard). Public visibility; default branch `main`; module `github.com/olegiv/my-code-dashboard`; executable `my-code-dashboard`; original code license **GPL-3.0-only**.

The empty repository was created on 2026-10-18 after an authenticated name-availability check. GitHub assigned repository ID `1424775093` and node ID `R_kgDOVOxXtQ`; [machine-readable metadata](repository-metadata.json) records the actual values and creation time. The local checkout is initialized on `main`, with `origin` pointing to this repository. Source publication awaits review and separate commit/push approval.

Before application publication, inspect every staged file for private runtime data, run the documented checks, and review both proposed commits explicitly. Real-provider verification for stable release uses appropriately permitted accounts and private evidence; it must not run as an automated code check. All example hosts/findings/screenshots in public source must remain generic/synthetic. Keep the GPL license and third-party notices with the source. Configure a private vulnerability-reporting channel before directing users to it.

Creation used the following command, without `--source`, `--push`, an automatic README, or an automatic license file, so it created no commit and published no source:

```sh
gh repo create olegiv/my-code-dashboard \
  --public \
  --description 'Self-hosted security dashboard for GitHub and GitLab repositories, built with Go, HTMX, and Alpine.js.'

gh repo edit olegiv/my-code-dashboard \
  --add-topic security-dashboard --add-topic vulnerability-management \
  --add-topic github --add-topic gitlab --add-topic golang \
  --add-topic htmx --add-topic alpinejs --add-topic self-hosted \
  --add-topic devsecops

# Retrieve actual assigned IDs and confirm metadata.
gh api repos/olegiv/my-code-dashboard \
  --jq '{repository_id: .id, node_id: .node_id, full_name, html_url, default_branch, topics}'
```

Review the current staged application candidate locally:

```sh
git status --short
git diff --cached --stat
git diff --cached
```

The publication plan now has two separate previews: a README/LICENSE-only bootstrap on `main`, then the complete application on `initial-implementation` for its first PR. Review each exact tree, diff and full commit message. The staged application must not be pushed directly to `main`. Follow [GitHub validation and the two-commit sequence](github-validation.md); all commits, pushes, PR creation and repository settings await separate authorization. Confirm the default branch and recorded metadata after the approved bootstrap push. GitHub cannot detect the source license until it is pushed.

GitHub **topics** are discoverability metadata. Git **release tags** identify reviewed source versions. `v0.1.0` was proposed on 24 October 2026 after real GitHub and self-managed GitLab validation and collection and a real backup-restore rehearsal; the maintainer waived the separate release review. Release notes live in [CHANGELOG.md](../CHANGELOG.md). Releases are created as GitHub drafts targeting a reviewed `main` commit, without binary assets, and publishing a draft creates its tag. The manual release workflow builds and verifies archives/checksums/source, then uploads a seven-day Actions artifact visible through the public run. It creates no tag or GitHub Release. The automatic PR package check keeps its archives on the runner and does not upload them. Distribute corresponding source alongside GPL binaries.
