#!/bin/sh
# Runs the e2e-tagged tests as root in a privileged golang:1.27.1 container,
# where they can mount filesystems: go test -race -count=1 -tags e2e ./...
# Extra arguments go to go test, before the package pattern, for example
#
#	scripts/e2e-docker.sh -run R1_9 -v
#
# The repository is mounted read-only, so the root-run tests leave nothing in
# it. Modules and build outputs persist in the precious-e2e-gomod and
# precious-e2e-gobuild volumes, so later runs download and compile only what
# changed. PRECIOUS_E2E_IMAGE overrides the image.
set -eu

repo=$(cd "$(dirname "$0")/.." && pwd)
image=${PRECIOUS_E2E_IMAGE:-golang:1.27.1}

exec docker run --rm --privileged \
	-v "$repo:/src:ro" -w /src \
	-v precious-e2e-gomod:/go/pkg/mod \
	-v precious-e2e-gobuild:/root/.cache/go-build \
	"$image" go test -race -count=1 -tags e2e "$@" ./...
