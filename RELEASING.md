# Releasing GoSaf

Every release follows this checklist, in order. Do not tag until every box is ticked.

1. **Changelog.** Move the `[Unreleased]` entries in `CHANGELOG.md` under a new
   `## [X.Y.Z] - YYYY-MM-DD` heading and add the compare link at the bottom. The release
   workflow publishes exactly that section as the release notes and fails if it is missing.
2. **Checks.** On the commit to be released:

   ```sh
   gofmt -l .        # prints nothing
   go vet ./...
   go tool -modfile=tools/go.mod staticcheck ./...
   go tool -modfile=tools/go.mod govulncheck ./...
   go mod verify
   go test -race -shuffle=on -count=1 ./...
   ```

3. **Performance gate.** GoSaf must not be slower and must not allocate more than gobreaker on the
   hot paths:

   ```sh
   cd bench && go run ./gate
   ```

   It exits non-zero if GoSaf's median ns/op is more than 2% above gobreaker's, or its allocs/op
   is higher, for "Closed, success" and "Open, reject". Run it on a quiet machine. If it fails,
   fix the code, do not loosen the gate. Only quote benchmark numbers in the docs if they come
   from this command.
4. **Dependencies.** Review open Dependabot pull requests. `go.mod` must still have no `require`
   lines; the `go` line must be the latest Go 1.x patch release.
5. **Signed tag.** Create an annotated, signed tag and verify it:

   ```sh
   git tag -s vX.Y.Z -m "vX.Y.Z: <one line summary>"
   git tag -v vX.Y.Z
   ```

6. **Push.** `git push origin main vX.Y.Z`. Pushing the tag starts the `release` workflow, which
   re-runs the checks and creates the GitHub Release from the changelog.
7. **After the release.** Confirm the workflow is green and the Release page shows the notes.
   Check that `pkg.go.dev` lists the new version.

Tags are never moved or deleted once pushed: the Go module proxy caches them. If a release is
wrong, publish a new patch version.
