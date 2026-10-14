# Security Policy

## Reporting a vulnerability

Please do not open a public issue. Use GitHub's private vulnerability reporting:
the repository's **Security** tab, then **Report a vulnerability**.

Include the affected version, a description, and a minimal reproduction if you have one.

## Supported versions

Only the latest release receives security fixes.

## Supply chain

GoSaf has no runtime dependencies. CI runs `govulncheck`, `staticcheck`, `go vet` and
`go test -race` on every change, and GitHub Actions are pinned by commit SHA.
