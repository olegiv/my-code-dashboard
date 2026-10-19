# Providers, permissions, and interpretation

These adapters read provider reports. Availability still depends on license, account role, token permission, organization policy, configured scanners, and instance version. Inspect **Connections** for validated identity/version and repository **Coverage & freshness** for actual results. No live private instance or user credential is part of automated validation.

## GitHub.com

| Source | Read API | Minimum fine-grained repository permission | Interpretation |
| --- | --- | --- | --- |
| Discovery/renames | `GET /user/repos`, `GET /repositories/{id}` | Metadata: read and selected repository access | One fine-grained token resource owner; add connections as needed. |
| Dependencies | `GET /repos/{owner}/{repo}/dependabot/alerts` | Dependabot alerts: read | Imports open, fixed, dismissed, auto_dismissed. No analysis timestamp is invented. |
| Code/SAST/SARIF | `GET …/code-scanning/alerts`, `GET …/code-scanning/analyses` | Code scanning alerts: read | Default branch scope; all returned lifecycle states. Tool/category streams are considered separately for evidence. |
| Default setup metadata | `GET …/code-scanning/default-setup` | Administration: read, optional | A missing default setup does not mean advanced setup is disabled. |
| Secrets | `GET …/secret-scanning/alerts?hide_secret=true` | Secret scanning alerts: read | Default stream plus an explicit generic-pattern stream covering nine selectors; both must complete before replacing the category. Severity may be unknown. |
| Containers, IaC, DAST, other | No dedicated adapter source | — | Explicitly unsupported as dedicated categories. SARIF tools appear under Code. |

The token owner's account must also have alert viewing access; repository administration/security-manager roles, organization approval, SSO, plan, and enabled security products can affect results. Prefer fine-grained read permissions over classic broad `repo` tokens. This app supports user PAT connections, not GitHub App installation-token renewal. Expiration requires replacing the connection token.

REST API version is pinned to `2026-03-10`. Dependabot responses are persistent alerts, not proof the dependency graph is complete. Code rule diagnostic levels (for example `error`) are preserved separately and never mapped to vulnerability severity. CodeQL default setup is optional evidence and requires no permission expansion for the base app to work.

Known, explicit GitHub error messages distinguish disabled code/secret scanners and missing default-branch code analysis from collection failures. Only bounded error JSON and exact allowlisted messages establish these states; arbitrary response text is discarded. Generic 403/404 responses remain denied or unavailable because they do not prove scanner enablement. Rate-limit responses take precedence. An empty successful alert list remains a collected report with independently unknown analysis evidence.

GitHub secondary rate limits can return HTTP 403 with `Retry-After` while the primary remaining count is nonzero. The app preserves that deadline, suppresses subsequent adapter requests and persists it for scheduled retries. Ordinary 403 permission failures remain denied. See [GitHub's rate-limit guidance](https://docs.github.com/en/rest/using-the-rest-api/rate-limits-for-the-rest-api).

The same disabled/missing-analysis classification applies to the code analysis endpoint after alerts have been read successfully. Such a response remains an incomplete snapshot: newly read alerts are discarded, earlier complete findings/analysis are retained, and activity records a coverage gap without scheduling collection-failure retries. The adapter keeps the requested default-branch ref on the error snapshot. Ambiguous errors and denied/expired credentials remain failures; rate-limit deadlines still take precedence. A successful empty analysis list remains distinct from an explicit missing-analysis error.

Generic secret selectors currently queried are HTTP basic/bearer authorization headers, MongoDB/MySQL/PostgreSQL connection strings, and OpenSSH/RSA/EC/PGP private keys. MySQL uses the documented API selector `mysql_connection_url`; see [GitHub's supported patterns](https://docs.github.com/en/code-security/reference/secret-security/supported-secret-scanning-patterns). The API's default response omits some generic types; AI-detected passwords, partner-only patterns, or newer provider categories are not claimed covered. Responses are decoded without literal secret/location fields even when an API ignores `hide_secret`. Native resolution reasons are retained. An inaccessible selector fails the category and preserves prior complete data; inspect provider settings/pattern eligibility rather than interpreting it as zero.

Setup guidance: in the provider, review Security settings for dependency graph/Dependabot, CodeQL or an existing SARIF pipeline, and secret scanning. Review each tool's configured schedule if daily scanning is desired. The dashboard's daily synchronization cannot make upstream scans run daily. The app does not change these settings.

Official references: [Dependabot alerts API](https://docs.github.com/en/rest/dependabot/alerts?apiVersion=2026-03-10), [code scanning API](https://docs.github.com/en/rest/code-scanning/code-scanning?apiVersion=2026-03-10), [secret scanning API](https://docs.github.com/en/rest/secret-scanning/secret-scanning?apiVersion=2026-03-10), [fine-grained token permissions](https://docs.github.com/en/rest/authentication/permissions-required-for-fine-grained-personal-access-tokens).

## Self-managed GitLab

Connect to your HTTPS instance, including an installation subpath if applicable. Use the OS/system trust store for a corporate CA; no TLS bypass is provided. A PAT with `read_api` is the default. The user normally needs Developer+ or an applicable custom security-report role, plus Ultimate security-report availability. Group/project access and instance feature flags still apply. No admin or broad `api` write scope is required by this design.

Discovery uses REST projects with keyset pagination, without a membership-only filter that would hide other accessible projects. The numeric project ID is used to refresh renamed/transferred project paths. Identity and available instance metadata/version are read first.

Persistent findings use **GraphQL**, not the deprecated REST vulnerability API. Authenticated introspection checks the exact instance for required vulnerability fields, project pagination, and all four lifecycle states: DETECTED, CONFIRMED, RESOLVED, DISMISSED. When introspection is unavailable, a minimal read-only report query verifies those fields, explicit lifecycle states, and pagination directly against the instance. Optional scanner metadata is queried separately and stays unknown if inaccessible. Authentication, transport, and rate-limit failures do not trigger this fallback. Queries use a fixed allowlist and variables for paths/cursors. Unsupported report queries, null projects/reports/nodes, partial errors, and missing/incomplete pagination fail visibly. Instance-specific schemas are not assumed equal to gitlab.com, and introspection does not need to be enabled to read a supported report.

If the optional scanner query is rate-limited after a complete vulnerability report was read, the findings still import successfully. Scanner state stays unknown, coverage explains the metadata limit, and the provider deadline is persisted for scheduled collection rather than discarded.

| Native report type | Dashboard category |
| --- | --- |
| Dependency scanning | Dependencies |
| SAST / Advanced SAST (`SAST_ADVANCED`) / SARIF | Code / SAST |
| Secret detection | Secrets |
| Container/cluster/registry image scanning | Containers |
| IaC / IaC scanning / SAST IaC (`SAST_IAC`), if exposed separately | IaC |
| DAST / API fuzzing / coverage fuzzing | DAST |
| Other or newly introduced report types | Other |

Actual scanner names are retained if the schema exposes them, otherwise the native report type is shown. Secret findings use a generic title; source/location/description fields are never requested. GitLab's existing persistent report usually represents default/tracked refs, not every branch or pipeline's ephemeral finding history. It can retain a detected vulnerability after a scanner no longer detects it; the dashboard preserves GitLab's lifecycle instead of inventing resolution.

Where supported, `Project.securityScanners { available enabled pipelineRun }` provides separate configured/latest-pipeline evidence. An enabled scanner absent from `pipelineRun` remains a coverage gap in the portfolio, repository details and refresh activity, even when its historical findings import successfully. This is not a failed API request and does not trigger collection-failure retries. It does not supply scan timestamps; a category with zero reported findings and unknown evidence stays unknown. IaC can be contained in SAST, and scanner lists are not a guarantee of distinct category parity. A category absent from the available list indicates unavailable standalone scanner evidence, not proof that no related analysis occurred.

The documented [`SecurityScannerType` values](https://docs.gitlab.com/api/graphql/reference/experimental/enums/#securityscannertype) `SAST_ADVANCED` and `SAST_IAC` map to Code and IaC respectively, for both scanner evidence and report types returned with those names. Observed, enabled, disabled and unavailable states retain their existing meanings; unavailable standalone IaC evidence remains unknown because IaC can also be included in SAST.

Coverage compares individual native scanner types before grouping them. For example, when DAST and API fuzzing are enabled but only DAST ran, the DAST category stays **enabled**, with a note naming `API_FUZZING` as missing. The same rule applies to multiple Code, Container or IaC scanner types. A successful sibling run cannot remove that gap from overview totals or activity. Complete active findings from either scanner remain visible; when every enabled type has run, the category becomes **observed**.

Setup guidance: use the project's **Secure / Security configuration** and **Vulnerability report**, inspect CI security report artifacts and default/tracked-ref pipelines, and have an administrator confirm role/license/features when the API is denied. Keep corporate scanners inside corporate CI infrastructure. This app does not download their artifacts, source, or pipeline logs or trigger jobs.

Official references: [GraphQL reference](https://docs.gitlab.com/api/graphql/reference/), [GraphQL security considerations](https://docs.gitlab.com/api/graphql/), [project vulnerability report](https://docs.gitlab.com/user/application_security/vulnerability_report/), [security scanner configuration](https://docs.gitlab.com/user/application_security/detect/), [PAT scopes](https://docs.gitlab.com/user/profile/personal_access_tokens/), [deprecated REST vulnerabilities API](https://docs.gitlab.com/api/vulnerabilities/).

## Status semantics

Successful collection, scanner enabled/disabled/observed/unavailable, unknown coverage, denied/expired credentials, missing features, unsupported endpoints/schema, failed collection, and stale analysis are different signals. Collection success is an API-read result, not a security score. No data is labelled secure. Historical analysis evidence may remain visible after a failed refresh, alongside its failed collection status.

For GitHub code scanning, both the alert list and analysis list must finish before replacing that category's snapshot. If analysis collection is denied, rate-limited or interrupted after alerts were read, retain the previous complete findings and analysis time/ref/commit, show the collection error and retry later. A successful empty analysis list remains unknown evidence without a fabricated timestamp. Pagination preserves literal delimiters inside provider Link URLs and quoted attributes; existing origin checks still apply to each page.

Analysis freshness uses each tool/category/ref stream's latest successful analysis and displays the oldest of those successes. A newer failed upload keeps the stream's earlier success in that calculation and marks coverage with a warning. A stream with no successful analysis contributes no timestamp; if none succeeded, analysis time remains **Not reported**. These calculations use only the analysis history returned by the provider.

Tool name, category and ref are independent stream identifiers, including any slashes inside them. A tool named `A/B` with category `C` stays separate from tool `A` with category `B/C`; neither can hide the other's warning or successful analysis evidence.

GitLab instances may specify HTTPS port 443 explicitly. Redirects, pagination and repository/finding links accept either its explicit or omitted form for the same hostname. Scheme changes, other hosts, user information and different effective ports remain rejected; TLS certificate verification is unchanged.

Collection is stale after 48 hours without success; supplied analysis evidence older than 48 hours is highlighted independently. Missing provider analysis timestamps are displayed as **Not reported**. Coverage notes retain explicit scope limitations.

**Activity** separates completed imports, disabled scanners, missing analysis, unsupported sources, and actual failures. Expand a refresh's repository/category details to see its saved reasons and links to repository coverage. The latest refresh with noteworthy outcomes opens automatically. Earlier versions saved only aggregate counts and may have counted coverage gaps as failures; those rows are explicitly labeled legacy results. No historical reasons are inferred from current coverage.
