# Precious build entry points. `go build` and `go test` never need Node: the
# UI is built into web/dist only by `make ui` (design D13).

PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64

.PHONY: ui build test test-slow e2e e2e-docker cross

ui:
	cd web/ui && npm ci && npm run build

build: ui
	go build -o bin/precious ./cmd/precious

test:
	go test -race ./...
	cd web/ui && npm test

# Adds the `slow` tests, among them R1.10 (a 2-million-entry scan, about
# 2 minutes plain and 30 under the race detector), hence the longer timeout.
# The R2 time and memory targets at 2 million entries (relate, Compare,
# review lists) and the R2b Search targets are meaningless under the race
# detector, so those tests skip in the first pass and run in a second pass
# without it.
test-slow:
	go test -race -tags slow -timeout 60m ./...
	go test -tags slow -timeout 60m -run 'TestRelateAtScale|TestReviewPagesStayFastAt2MillionEntries|TestSearchStaysFastAt2MillionEntries' ./internal/relations/ ./internal/review/ ./internal/web/api/

# The browser suite (web/ui/e2e): builds the UI and the binary, serves the
# regression corpus, and drives Chromium through the R1 flows. It needs Go,
# Node, util-linux script(1), and Playwright's Chromium
# (`npx playwright install chromium` once).
e2e:
	cd web/ui && npm ci && npm run e2e

# The e2e-tagged tests as root in privileged Docker, where they can mount.
e2e-docker:
	scripts/e2e-docker.sh

cross:
	@for platform in $(PLATFORMS); do \
		os=$${platform%/*}; arch=$${platform#*/}; \
		ext=; if [ "$$os" = windows ]; then ext=.exe; fi; \
		echo "go build $$os/$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -o bin/precious-$$os-$$arch$$ext ./cmd/precious || exit 1; \
	done
