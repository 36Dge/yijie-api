# FEAT-153 editor static artifact interface

The deployment authority is `config/workflow-editor-assets.schema.json`. Coze produces a
canonical production bundle with base/publicPath `/editor/` and a root `manifest.json`.
Only `index.html` and named `assets/` JS/CSS/images/fonts are publishable; source maps, private
configuration, manifests, arbitrary JSON and source files are excluded. Scripts are external,
with no eval, worker, CDN or runtime schema compilation requirement.

The manifest records schema version 1, base path, source_repository `yijie-coze`, base commit,
deterministic candidate source digest, SHA-256 of the exact canonical Contracts
`compatibility/workflow-local/source.lock.json` bytes, and each asset's relative path, SHA-256,
uncompressed byte count and exact MIME from the schema. It must list `index.html` once, contain
unique paths, at most 512 files, at most 16 MiB per file and at most 64 MiB total; manifest size
is at most 256 KiB. Infra records and supplies the manifest's SHA-256 separately after verifying
the artifact source/build evidence, and mounts the directory read-only.

API uses opt-in `YIJIE_WORKFLOW_EDITOR_ENABLED=true`, fixed
`YIJIE_WORKFLOW_EDITOR_BUNDLE_DIR=/opt/yijie/workflow-editor` and
`YIJIE_WORKFLOW_EDITOR_MANIFEST_SHA256=<verified lowercase hash>`. Disabled mode retains the
existing API behavior. Enabled mode fails startup if the manifest, compiled source lock,
regular files, sizes, MIME or hashes differ. It preloads the verified bytes into a bounded
immutable map, so later requests never select a filesystem path or observe a partial rebuild.
Build/update the artifact and restart the owned service normally; do not change a live bundle.

The top-level gateway serves only GET/HEAD `/editor/` and its exact manifest-listed paths.
It has no directory listing, generic SPA fallback, proxy, upload or credential endpoint.
The existing `/v1/` handler keeps all K_NA, epoch and Origin checks. HTML and assets use
`Cache-Control: no-store`, `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer`
and the fixed editor CSP: no network connections, external/inline scripts, workers, frames,
objects, forms or base URL changes. Inline styles and same-origin/data images/fonts support
the existing editor UI. `frame-ancestors` allows only `http://localhost:1420` and
`tauri://localhost`; actual dev/packaged WK behavior remains a qualification requirement.
No X-Frame-Options header contradicts that cross-origin embedding policy.

No resource, actor, E, token or DSN enters an asset, URL, manifest or browser configuration.
The parent uses the existing generated connect/ready bridge with a transferred MessagePort,
and existing generated workflow requests/responses. Parent owns the E deadline from
EditorOpenedView; explicit reconnect changes bridge/generation/port without reloading the
iframe. Dirty canvas and its original base revision stay in the editor until successful save
or explicit user resolution; a changed server revision cannot silently replace that base.
