# Contributing

## Development

```sh
go build ./...
go test -race ./...
go vet ./...
golangci-lint run ./...
./scripts/check-layering.sh
```

`internal/release` holds the domain logic and never talks to the network directly.
`internal/publisher` (GitHub and GitLab APIs), `internal/pusher` (git transport) and
`internal/repository` (forge detection and credentials) are its ports, wired together in
`cmd/magic-releaser`. `scripts/check-layering.sh` enforces this in CI.

## Commits

Use [Conventional Commits](https://www.conventionalcommits.org/): `feat:` and `fix:` release a new
version, other types do not.

## Releases

magic-releaser releases itself: the release workflow runs the action on `main`, GoReleaser attaches
the binaries and publishes the container image, and the `v1` branch moves to the new release.
