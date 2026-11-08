# Development Guidelines

## Working approach

- Communicate concisely in Japanese. Keep README.md and AGENTS.md in English.
- Read README.md and the relevant implementation before changing behavior.
- Work only in this repository. Do not read secrets or .env files.
- Keep changes minimal and scoped to the request; avoid unrelated refactoring.
- Keep the existing UI appearance and interactions; do not redesign it without permission.
- Prefer simple Go standard-library code, without unnecessary dependencies or abstractions.
- Do not commit, push, merge, deploy, or release unless explicitly instructed. Use concise English Conventional Commits.

## Structure

- `cmd/speedtest`: server/client/benchmark/healthcheck commands; platform-specific process CPU accounting.
- `internal/speedtest`: shared configuration, results, calibration, validation, HTTP server/client, TCP/UDP transport, and Go tests.
- `web.go`: embeds only index.html, LICENSE, and assets into the executable.
- `index.html`, `assets/css`, `assets/icons`: the supported UI and retained visual assets.
- `assets/js/{ui,results,measurement-core}.js`: UI, display smoothing/formatting and saved settings.
- `assets/js/measurement-worker.js`: independent browser HTTP transport, outside the UI thread.
- `assets/js/connection.js`: verified IPv4/IPv6 routing helpers.
- `tests`: Node built-in tests for UI and Worker integration with the Go executable.
- `Dockerfile`, `compose.yaml`: static Go server in a non-root image.

## Measurement rules

- Keep transport and UI processing separate. Preserve the Worker message contract.
- Preserve one common result/config schema for Web and CLI.
- Go serves calibration constants through /api/spec; keep browser and CLI algorithms consistent.
- Independent directional calibration, then fixed concurrency. Manual never changes counts.
- Warm-up/probes use separate transfers. Count receiver-confirmed payload only.
- Report actual elapsed time including acknowledged tails/drain, never guessed line rates.
- Reuse buffers, bound sessions/transfers/timeouts, and cancel every owned operation.
- UDP needs authenticated stream capabilities, sequence deduplication, bounded pacing and reliable final control statistics.
- Never smooth, interpolate, round, or compensate the measured totals; smoothing belongs only in the display.
- Serve only embedded public assets, never repository source or configuration.
- Preserve applicable third-party copyright/license notices.

## Verification and delivery

- Run relevant Go tests and go vet. For network changes run TCP/UDP/HTTP integration tests.
- Build the local executable before `node --test tests/*.test.cjs`.
- Use `go test -race ./...` where CGO and a C compiler are available.
- After browser changes, check real measurements, cancellation, settings, copy/retry, desktop/mobile, and Light/Dark. UI tests do not replace visual checks against the original CSS, markup, and screenshots.
- Use `speedtest benchmark` for measured performance; retain conditions and raw JSON. Distinguish browser/native, loopback/remote, and Docker/native.
- Do not claim 100–200 Gbps without corresponding measurements. Distinguish runtime heap from process RSS and client-only from combined resource use.
- Keep standalone and Docker startup working. CI covers Windows, Linux, macOS, and Linux Docker.
- Update existing documentation when behavior changes. Avoid history, migration notes, unnecessary documents, and speculative bottleneck claims.
- Review the final diff; briefly report changed files, checks, measured results, limitations, and unrun tests.
