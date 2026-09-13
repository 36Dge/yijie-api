# FEAT-153 independent local workflow API

The step-4 controlled stack and real PostgreSQL/MySQL qualification are recorded in the
meta repository's FEAT-153/07. Step-5 static editor hosting is implemented; real Desktop
editor qualification remains separate and has not passed.

For a headless, ordinary API session lifecycle check, build with
`make workflow-qualify-build`, then run the existing qualifier with
`--session-lifecycle --evidence /absolute/new.json` under the same four exact local gates
and an absolute, clean `YIJIE_WORKFLOW_CREDENTIAL_FILE` path. No TTL/clock/URL override
is accepted. It creates a named synthetic draft, reads at 150 seconds, observes natural
expiry after 301 seconds, checks explicit new-session and normal close revocation, and
confirms resource/create-receipt preservation. It never saves, tests, publishes or runs
a workflow. `make workflow-qualify-check` runs only fast in-process checks.

The evidence excludes session secrets, private headers and session DTOs. API lifecycle
success cannot establish native memory cleanup, MessageChannel, draft preservation or
dev/packaged WebKit qualification. Use the canonical Desktop entry for those checks.

This source-first local candidate implements an independent API process. It does not change
`cmd/api-server`, OIDC, legacy/secure Tasks, `public-api.lock.json`, or the existing migration version.
The API owns fixed-scope resource associations, durable dispatch receipts and append-only audit.
Coze owns drafts, revisions, versions, root executions and node results. No canvas or execution
state machine is copied into API PostgreSQL.

## Configuration

The credential file authority is `config/workflow-local-runtime.schema.json`. Both files contain
`schema_version: 1`, a canonical UUID `run_epoch`, and a 64-lowercase-hex `token`. They must be
regular, non-symlink, single-link, current-owner files of at most 1 KiB, created with 0400/0600 permissions;
do not change permissions to manufacture a failure. K_NA and K_AC must have the same epoch and
different tokens. Infra creates these files and mounts them separately; no token belongs in
argv, renderer variables, logs, source examples or Host/Runtime environment.

Required environment:

| Name | Value or source |
|---|---|
| `YIJIE_ENV` | `local` |
| `YIJIE_LOCAL_PROFILE` | `demo_fast` |
| `YIJIE_WORKFLOW_ENABLED` | `true` |
| `YIJIE_API_SERVICE_PROFILE` | `feat-153-workflow-local` |
| `YIJIE_WORKFLOW_CREDENTIAL_FILE` | Absolute path to K_NA JSON |
| `YIJIE_WORKFLOW_COZE_CREDENTIAL_FILE` | Absolute path to K_AC JSON |
| `YIJIE_WORKFLOW_COZE_URL` | `http://coze-workflow:18889` (also the default) |
| `YIJIE_WORKFLOW_POSTGRES_DSN_FILE` | Preferred deployment input: absolute owner-only file, non-symlink, single-link, 0400/0600, at most 4 KiB; contains the DSN and optional surrounding whitespace; read only in process |
| `YIJIE_WORKFLOW_POSTGRES_DSN` | Direct environment compatibility input, mutually exclusive with the file; fixed `postgres` scheme, nonempty credentials, `workflow-postgres:5432`, database `yijie_workflow_local`, sole query `sslmode=disable` |
| `YIJIE_WORKFLOW_NETWORK_SCOPE` | `host`/absent binds `127.0.0.1:18888`; `container` binds `:18888` inside the isolated container |

Container mode requires Infra's sole host publication `127.0.0.1:18888:18888`. It is not permission
to expose the listener publicly. The original Coze server and its dependencies are not published
as additional host ports. Runtime upstream URLs are fixed; tests use only ordinary httptest servers.

The Coze adapter requires these explicit deployment inputs in addition to its local/demo_fast
gates and separate K_AC mount:

| Name | Coze container value or constraint |
|---|---|
| `YIJIE_WORKFLOW_NETWORK_SCOPE` | `container` |
| `YIJIE_WORKFLOW_COZE_LISTEN_ADDR` | `0.0.0.0:18889`, internal only; the default remains `127.0.0.1:18889` even when network scope is container |
| `YIJIE_WORKFLOW_MYSQL_DSN_FILE` | Preferred Coze DSN input; absolute owner-only private file, at most 8 KiB, mutually exclusive with `MYSQL_DSN` |
| `MYSQL_DSN` | Runtime-injected credentials; `tcp(coze-workflow-mysql:3306)/coze_workflow_local` with required `parseTime=true`; no multiStatements or custom/global TLS registrations |
| `YIJIE_WORKFLOW_REDIS_PASSWORD_FILE` | Private file, at most 4 KiB; mutually exclusive with `REDIS_PASSWORD` |
| `YIJIE_WORKFLOW_MINIO_ACCESS_FILE` / `YIJIE_WORKFLOW_MINIO_SECRET_FILE` | Private files, each at most 4 KiB; mutually exclusive with `MINIO_AK` / `MINIO_SK` respectively |

Without the explicit Coze listen address the API container cannot reach the adapter; omitting
`parseTime=true` causes Coze configuration validation to reject startup. These inputs are recorded
for step 4; no container or database has been started to qualify them in step 3.

The DSN file uses the same descriptor ownership/no-follow/stability checks as machine credentials.
The process never copies its contents into `os.Environ`, command arguments, logs or HTTP output.
Compose must pass only the file path; interpolating the DSN into container environment defeats
the purpose of this deployment input.

## Build and source verification

The reader uses `O_NOFOLLOW | O_CLOEXEC`, validates descriptor ownership/link count and verifies
the descriptor and path remain stable after reading. Tests only create normal owner-only files;
they do not inject symlinks or alter file permissions.

`make workflow-generate-check` uses the Contracts canonical consumer checker. The separate local
candidate lock records base SHA plus exact sources/generated digests, and is not a release tag.
Never run the old public generator against a mismatched sibling HEAD or edit generated types.

```sh
make workflow-generate-check
make workflow-lint
make workflow-test
make workflow-build
```

`WORKFLOW_GO` can point to an already installed Go toolchain meeting go.mod. Build outputs are the
canonical reproducible project files under ignored `bin/workflow-local/`; no existing Runtime or
signed application binary is replaced.

## Lifecycle and operation semantics

`workflow-local-migrate up` explicitly applies only the independent workflow migration book.
The server never migrates automatically. Server startup checks that book and four tables, normally
ensures the fixed principal through the authenticated Coze adapter, transactionally binds it in
API PostgreSQL, and only then opens its listener. Native does not need a manual principal POST.

Every API request needs `Authorization: Bearer <K_NA>` and `X-Yijie-Run-Epoch`. No credentialed
browser CORS surface is registered. Editor bootstrap/save/test/publish also need
`X-Yijie-Editor-Session`; E is random, resource-bound, memory-only and expires after 300 seconds.
Opening returns 201 with the generated `EditorSessionSecret`; native retains it and never passes
it to renderer. Closing uses machine authentication only and remains idempotent after expiry.
Closing/epoch change invalidates E but does not cancel an existing engine run.

Create/save/test/publish/run first claim a durable operation intent. Matching retries query
receipts rather than submit another write; changed content with the same ID conflicts. Unknown
delivery remains queryable without invented completion or automatic restart. A completed receipt
means registration committed, not successful execution. Run status and summary history are read
from Coze. Responses validate against the consumed source and stay below 512 KiB, including JSON
escaping. A response-construction failure after a write returns `operation_unknown` and the original
operation ID, never a false definitive input rejection.

If an old completed create/save/publish intent is retried after the draft revision changed, this
candidate returns `operation_unknown` with its original ID rather than return a newer draft as the
old result. The operation query still exposes its real completed receipt. This avoids a second
API canvas store; historical full-draft replay requires an explicit provider/source extension.

SIGTERM/Interrupt starts normal `http.Server.Shutdown` without forced Close. After 30 seconds a
safe `shutdown pending` message is emitted while normal draining continues. Infra owns the full
stack and must use non-forcing container stop semantics, preserve volumes, and not clear an
in-progress Coze slot based on API HTTP timeout. The static editor is not implemented in step 3.

## Verification scope

Safe focused tests cover ordinary creation/replay/conflict, recorded-to-completed reconciliation,
native-only session expiry/close, source-conformant handlers/client, unknown run semantics, list
summaries, ordinary input bounds and response serialization bounds. Test repositories are unit
ports only and are not presented as real persistence evidence.

Real PostgreSQL migration/transaction/append-only checks, Coze integration, process readiness,
editor/WebView and normal stack stop/reopen remain NOT RUN until the Infra and real qualification
steps. Historical default tests containing unsafe fixtures, permission-fault or attack scenarios
are not executed. No claim of full-repository, production, public or D4 completion is made here.

## Step 4 controlled qualification

`Dockerfile.workflow-local` requires Infra's digest-pinned `GO_IMAGE`; there is no floating default.
Build from this repository as context. The dedicated Dockerignore only admits the local workflow
Go source, its migration SQL, generated contract/schema and minimal database package. It excludes
private configuration, artifacts and unrelated historical tests. `workflow-runtime` is scratch
with reproducible static server/migrate outputs; `workflow-test-runner` is an opt-in Go builder
stage. The latter never runs a test while building. Infra sets the process UID/GID to match the
new owner-only credential/DSN mounts and publishes only host `127.0.0.1:18888`.

The same server binary provides `--check-ready` for the scratch image healthcheck. It reads the
private local configuration and makes one three-second authenticated GET to the fixed
`127.0.0.1:18888` status route, requiring source-conformant ready/state/protocol/epoch. It does
not initialize the service, migrate or bind a principal, and accepts no caller-selected URL.

Infra owns this execution order:

1. Start only the dedicated databases/dependencies, then explicitly run
   `/usr/local/bin/workflow-local-migrate up` against `yijie_workflow_local`.
2. Start Coze and API normally. API must have persisted the real Coze principal before qualification.
3. On the host, run `make workflow-qualify-build` (includes canonical source/consumer checking),
   then `bin/workflow-local/workflow-local-qualify --evidence <new absolute path>` using the exact
   local profile and host-owned K_NA file. No K_AC or DSN is required by the HTTP tool.
4. The HTTP tool creates one declared text workflow, saves/replays/CAS-checks it, tests and
   publishes `v0.0.1`, changes the text prefix and publishes `v0.0.2`, then publishes the same
   second revision under a fresh operation as `v0.0.3`. It executes both `v0.0.2` and the older
   `v0.0.1`, checks all four real runs/node results and paged history, and closes E normally.
   The artifact contains generated source DTOs and attempted operation IDs, never K_NA/E.
   Failure writes `complete: false`; it never blindly retries an unknown write or invents success.
5. Run the `workflow-test-runner` on only Infra's dedicated network, mounting K_NA, K_AC and the
   private DSN file under the same UID. Set `YIJIE_WORKFLOW_PG_QUALIFICATION=normal` and
   `YIJIE_WORKFLOW_QUALIFICATION_WORKFLOW_ID` to the HTTP-created workflow ID. The separate
   `workflowintegration` tag selects one normal PG test. It checks the explicit migration book,
   real fixed principal/resource, eight concurrent claims, a declared recorded→unknown storage
   receipt, normal audit insertion/readback and connection reopen. It does not run migration,
   create a fake Coze principal, claim execution success, clear tables or mutate audit records.
   Append-only behavior is checked by reading the enabled trigger/function definition.
6. After Infra's normal whole-stack stop/reopen and epoch rotation, rerun the host tool with
   `--verify-evidence <same absolute path>` and the new K_NA file. This mode makes authenticated
   reads only and verifies the persisted workflow, real executions, receipts and history.

Database process/volume persistence is established by the final normal stack reopen, not the
PG test's shorter connection-reopen check. These commands remain opt-in; merely building or
reading this document does not authorize a second stack, unknown database or forced shutdown.
