# Tests and code review

The workflow adapts [ocms-go's test gate](https://github.com/olegiv/ocms-go/blob/09c2bd1f2b010f48fad2e45ca5c7089baef7b9c1/scripts/test-gate.sh), [pre-push hook](https://github.com/olegiv/ocms-go/blob/09c2bd1f2b010f48fad2e45ca5c7089baef7b9c1/.githooks/pre-push), and lint rules. Local checks require Go, Git, Bash and Make. No Node.js or Docker is required. Pinned Go tools download automatically; missing tools and failed checks stop the gate.

## Local checks

| Command | Checks |
| --- | --- |
| `make build` | Compile with Go 1.26.9 |
| `make test` | All packages with the race detector and `-count=1`, bypassing cached test results |
| `make lint` | gofmt and golangci-lint v2.15.0: staticcheck, errcheck, errorlint, govet, revive, gocritic, duplication and complexity checks |
| `make check` | Tests, lint, vet, tidy diff, module checksums, govulncheck v1.9.0 and browser checksums |
| `make commit-check` | Export the exact index to a temporary directory and run `make build check`; reject an empty index or an index changed during checks |
| `make pr-check` | Check the exact public index against the release manifest, then ShellCheck, actionlint v1.7.12 and Gitleaks v8.30.1 in a temporary export; requires ShellCheck on PATH |
| `make package-check` | Build four target archives and corresponding source, then verify checksums, source/documentation bytes, binary metadata, component notices and the native version |
| `make install-test-gate` | Install the repository pre-push hook, preserving existing hooks and global Git configuration |

`make check` checks the working tree. Before the first application commit, stage the intended public files and run **`make commit-check`** and **`make pr-check`**. Both work before HEAD exists; unstaged changes and ignored private data are excluded. Checks create no commit and make no push. ShellCheck is an additional development tool (`brew install shellcheck`, MacPorts `port install shellcheck`, or Debian/Ubuntu `apt install shellcheck`); a missing checker fails the gate. Packaging is separate: run `make package-check` and inspect target-specific upstream notice contents before a release. This command checks the working tree against the manifest, so run it on an export of the same candidate reviewed by the staged gates. The temporary export is source selection, not a security sandbox for untrusted PR code.

Tests use synthetic providers, loopback servers and temporary databases. Gate tests use disposable Git repositories; fixture commits exist only there, to test clean pushes, wrong refs, staged/working-tree differences that cancel against HEAD, dirty files, failed checks and deletion-only pushes. Tests also cover the unborn index, exclusion of local files, index changes during checks, idempotent hook installation and existing hook/configuration preservation. They do not access real provider credentials or runtime installations.

Restore regression tests use synthetic archives and an observed in-memory credential backend. They cover shadow identities, SQLite BLOB/NULL keys, JSON representations, unsupported schema objects and triggers that clear quarantine immediately or on refresh. All five application record tables are checked for malformed data and mismatches between decoded identities/indexed fields and native SQLite columns before destination replacement, including history beyond UI limits. Rejected archives leave the destination byte-for-byte unchanged. Matching duplicate, case-aliased and escaped selection fields remain safely deselectable/reselectable after restore, preserve metadata, stay excluded from the next refresh and produce a valid subsequent backup. The provider factory stops before any network request. Genuine backups preserve selections, findings and history, and remain blocked from credential lookup after restart; the engine suite also verifies explicit reconnection. Run the focused boundary checks with `GOTOOLCHAIN=go1.26.9 go test -race -count=1 ./internal/store ./internal/platform ./internal/engine` before the full staged gate.

Web regressions render normal and crafted imported connection IDs, check that discovery/removal actions remain fixed, and exercise escaped hidden IDs, POST-body precedence, credential quarantine and CSRF. Reconnection tests reject provider/URL mismatches before the fake provider is called, retain quarantine on rejection, and accept explicit matching recipients. Browser verification also checks recipient selection, guidance and token clearing, the effective HTMX 4 configuration (`includeIndicatorCSS: false`), strict CSP, Alpine selection and HTMX filter/refresh responses. Engine tests persist rate-limit deadlines from manual refresh across restart, daily-slot and schedule preference changes without consuming scheduled attempts, then verify collection resumes after the deadline. GitLab fixtures separately verify that optional scanner-query rate limits preserve complete findings and propagate backoff while unavailable metadata leaves coverage unknown.

Repository-view tests keep retained active findings visible after deselection through both the detail page and its explicit findings filter, while portfolio totals and default findings remain limited to monitored repositories. History fixtures mix UTC and offset-bearing timestamps at exact seconds and nanosecond boundaries, checking both newest-first ordering and exact retention cutoffs for runs and changes.

HTMX history-restoration requests receive the complete page shell with navigation and filters. Ordinary filtering and pagination still receive only the findings fragment. A dedicated response regression and synthetic browser Back/Forward flow check these separate consumers.

Secondary-limit fixtures return 403, a nonzero primary remaining count, and both forms of `Retry-After`; they verify classification, deadlines and suppression of later requests. Existing engine deadline tests cover persistence across restart and daily slots. Custom schedule fixtures cover Zurich and New York skipped/repeated times; an engine regression proves yesterday's consumed slot is not replayed before the shifted spring slot and that the new slot runs once.

Channel-controlled discovery tests overlap both successful and failed responses with credential replacement/removal. They verify the current identity/name/status survives and stale repositories are not saved. Unchanged connections still accept successful discovery and display ordinary errors. These fixtures use memory credentials and fake providers only.

Repository metadata fixtures exercise successful HTTP responses containing null, empty objects, missing/nonpositive IDs, missing paths and mismatched immutable IDs for both providers. Rejection retains prior metadata; valid renames, branch and archive changes still import. Store and engine regressions independently reject changed external IDs, preserve active findings and historical repository names on failure, and validate backups produced after rejected updates and normal renames.

Release tests use a fixture-local Go stub and disposable source tree to prove that unlisted documents and Go files do not reach builds or archives, while listed documentation remains included. Invalid, duplicate, missing, traversal and symlink manifest entries stop preparation. Separately run the real release script, inspect all four binaries and the source archive, verify checksums and target-specific notices, and compare packaged source with the reviewed [file manifest](../scripts/release-files.txt). Keep that manifest current when adding public source files.

PR gate fixtures exercise all three checkers, failure propagation, manifest drift, index changes and exclusion of unstaged/private files without creating a project commit. Archive-verifier fixtures accept matching source and reject changed/unlisted files, missing source/notices, path traversal, links, AppleDouble metadata and malformed binary metadata. Real packaging additionally verifies all cross-target build metadata and the current host's version command; foreign-target binaries are not executed locally.

The pre-push hook requires each non-deletion ref to point to HEAD and rejects staged, unstaged and untracked changes, then runs the staged gate. Deleted refs do not run tests. A global `core.hooksPath` must chain to `.git/hooks/pre-push`; the installer reports this requirement and preserves the configuration. Hooks are local guards; CI is still required.

## Codex and Codex Security

Before approving the first application commit, run both against the candidate that passed the checks:

1. An independent, ephemeral, read-only Codex code review for bugs and regressions. Without an initial HEAD, review an exported staged directory. Do not create an artificial project commit to supply a review base.
2. A **Codex Security Standard scan** covering credentials, untrusted provider responses, private storage, local HTTP protections and distribution. `govulncheck` checks known Go vulnerabilities; it does not replace this source review.
3. Validate findings, fix confirmed blockers, rerun affected checks and refresh review evidence after relevant changes. Keep reports and the tree ID in ignored output or the security workbench. Missing, blocked and incomplete reviews remain pending, even if a wrapper exits successfully.

AI reviews are development checks; the app and ordinary Go tests require no AI service. Record actual review results and coverage limitations in the commit preview. Record direct normal/failure behavior and affected consumers separately from baseline checks; see [validation](validation.md).

Hosted review setup, actual completion checks and access prerequisites are described in [GitHub validation](github-validation.md). They apply to the first application PR as well as later PRs. Never store AI API keys in this public repository or its workflows.

## GitHub checks

**Go checks** runs `make build check` on macOS and Linux. **PR quality** checks workflows, shell scripts, secrets and release archives. **CodeQL** separately analyzes Go, browser JavaScript and Actions. Dependency review compares PRs targeting `main` with their base. Actions use full commit SHA pins; pinned Go tools use the module checksum infrastructure. GitHub manages Actions' internal JavaScript runtime; the project has no npm dependencies or frontend build requirement. See [the complete check matrix and two-commit publication sequence](github-validation.md). A README/LICENSE-only bootstrap has no application checks; all code is introduced by the subsequent PR.

Verify actual hosted results after the authorized push/PR. Local success cannot establish hosted status, Linux runtime behavior or compatibility with every provider version. Commit, push, repository settings and publication each require the user's authorization.
