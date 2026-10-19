# Third-party software

Original application code is GPL-3.0-only. The components below retain their original licenses and notices. Browser assets are embedded without modification; no CDN is used at runtime.

| Component | Version | License / retained notice |
| --- | --- | --- |
| Go standard library | Go 1.26.9 build toolchain | BSD-3-Clause |
| modernc.org/sqlite | v1.60.1 | BSD-3-Clause; embedded SQLite public-domain code, sqlite-vec MIT, and other notices supplied by the module |
| github.com/zalando/go-keyring | v0.2.8 | Apache-2.0 |
| HTMX + Alpine compatibility extension | 4.0.0 | BSD-0-Clause; `internal/web/assets/vendor/HTMX-LICENSE` |
| Alpine CSP | 3.17.4 | MIT; `internal/web/assets/vendor/ALPINE-LICENSE` |
| Vue reactivity included in Alpine | 3.5.30 upstream notice | MIT; `internal/web/assets/vendor/VUE-LICENSE` |

Pinned browser downloads and SHA-256:

| File | Upstream | SHA-256 |
| --- | --- | --- |
| `htmx-4.0.0.min.js` | [htmx.org@4.0.0/dist/htmx.min.js](https://cdn.jsdelivr.net/npm/htmx.org@4.0.0/dist/htmx.min.js) | `e484d9171a9db30a39c8f16e3d709d4137f3211c659f8e6125816635033d593f` |
| `hx-alpine-compat-4.0.0.js` | [htmx.org@4.0.0/dist/ext/hx-alpine-compat.js](https://cdn.jsdelivr.net/npm/htmx.org@4.0.0/dist/ext/hx-alpine-compat.js) | `0984507114aaf26a4461dc74173cd0e1977a971ab8a33843042e683ab871f69a` |
| `alpine-csp-3.17.4.min.js` | [@alpinejs/csp@3.17.4/dist/cdn.min.js](https://cdn.jsdelivr.net/npm/@alpinejs/csp@3.17.4/dist/cdn.min.js) | `0d18d7f8d7910e2e0212f0f056b12f50bebc3abb7d88d2f7c7cb4c336fe4519a` |

`bash scripts/release.sh 0.1.0-dev` generates `THIRD_PARTY_LICENSES.txt` inside each binary archive. The generator reads that executable's Go version, target platform, and dependency metadata, then collects upstream notices from its selected packages and their parent directories. It includes Go's license, applicable nested notices, and the three browser license texts above. Test-only modules, compiler tools, unrelated subdirectories, and logo-only notices are excluded. Missing notices or mismatched dependency/toolchain versions stop packaging.

Generated notice files stay in release archives under ignored `dist/`; there is no checked-in license inventory or generated license directory. The separate corresponding-source archive uses `go mod vendor` to retain dependency source and its upstream license metadata, and includes the browser license files beside their assets. Review notices when dependencies or embedded code change; automated selection does not determine license compatibility. Packaging does not publish artifacts.

The prepared GitHub workflows pin official `actions/checkout` v7.0.1, `actions/setup-go` v7.0.0, and `actions/upload-artifact` v7.0.2 to verified commit SHAs (checked 2026-10-18). Those hosted CI actions are infrastructure and do not add a Node.js requirement to this Go application's build or runtime. Browser scripts are maintained directly; there is no npm package manifest or frontend build step.
