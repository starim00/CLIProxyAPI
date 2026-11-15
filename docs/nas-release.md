# NAS release verification

Use `Dockerfile.nas` for routine NAS releases. Do not copy historical timestamped
Dockerfiles that run the entire test suite inside every image build.

## Select verification by the change

| Change | Verification before promotion |
| --- | --- |
| Management UI only | Frontend `bun run verify:release`, changed-feature behavior tests, one browser flow. No Go build or container restart. |
| Key/configuration fields | `scripts/verify-release.sh config`, plus the changed UI flow when applicable. |
| Anti-truncation, embeddings, TTS | `scripts/verify-release.sh custom`, plus tests for any modified executor outside that selection. |
| Credential selection / cooldown | `scripts/verify-release.sh auth`. |
| Broad executor / protocol changes | `scripts/verify-release.sh executor`. |
| Broad upstream merge / cross-cutting changes | Full suite once in Linux CI or the NAS `validation` target. |
| Tests / documentation / release scripts only | Run affected tests or syntax/dry-run checks. Do not restart production. |

Never use a passing narrow suite to claim the full suite passed. New behavior must
be covered by an appropriate targeted test even if it is not in a named scope.
If the exact revision already passed suitable Linux CI, reuse that result rather
than running the same suite on Windows and again on the NAS.

## Build once

Keep the prior production image as `RUNTIME_IMAGE`. Use a clean source archive of
the commit being released. Keep the NAS Go build cache; do not use `--no-cache`.

```sh
docker build -f Dockerfile.nas --target validation \
  --build-arg RUNTIME_IMAGE="$previous_image" --build-arg TEST_SCOPE=config .
docker build -f Dockerfile.nas --target runtime \
  --build-arg RUNTIME_IMAGE="$previous_image" \
  --build-arg VERSION="$version" --build-arg COMMIT="$commit" \
  --build-arg BUILD_DATE="$build_date" -t "$candidate_image" .
```

Validation and runtime builds use the same source archive. The runtime target
compiles the server but does not rerun tests. It inherits the installed management
page and runtime libraries. Publish an updated management page separately only
when frontend code changes; preserve the configured fork update source.

## Candidate and live checks

1. Back up compose, configuration, credentials, image identity and model IDs.
2. Start the candidate on a loopback-only port with copied configuration and
   credentials. Check readiness and model IDs; run one real request for each
   changed execution path. For example, a key-label edit does not require TTS
   and four embedding calls. For a broad upstream merge, test each custom
   feature family once, including prefixed anti-truncation.
3. Promote the exact candidate image. Verify its revision, running/restart state,
   preserved mounts/ports, and model IDs. For UI changes check the served HTML
   hash. Make one representative live request, not the entire candidate matrix.
4. Restore the previous compose/configuration/image if the required live check
   fails. Preserve upstream failures in the evidence; do not silently skip them.

Record test, build and smoke-test durations separately. Do not promise a fixed
release duration: cold compilation and upstream API latency vary.
