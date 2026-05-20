# Convenience targets for dredd development and testing.

GO       ?= go
DOCKER   ?= docker
BIN_DIR  ?= bin

.PHONY: build
build:
	$(GO) build -o $(BIN_DIR)/dredd        ./cmd/dredd
	$(GO) build -o $(BIN_DIR)/dredd-build  ./cmd/dredd-build
	CGO_ENABLED=0 $(GO) build -o $(BIN_DIR)/dreddagent ./guest/dreddagent

.PHONY: test
test:
	$(GO) test ./... -count=1

# Build the dredd runtime image and the setup-only dredd-build image.
.PHONY: image
image:
	$(DOCKER) build --target dredd       -t dredd:latest       .
	$(DOCKER) build --target dredd-build -t dredd-build:latest .

# Build the language-test custom images (used by the docker-language tests
# for languages without a usable public image).
.PHONY: test-images
test-images:
	./dreddtest/images/build.sh

# Run the comprehensive all-languages test inside Docker. Every entry in
# the matrix MUST run and pass — no skips. Takes a long time on first run
# because it pulls every per-language image.
#
# Run `make test-images` first to build the custom images for languages
# without public images (NASM, COBOL, FreeBASIC, Kotlin 1.3, Objective-C,
# FPC).
.PHONY: test-languages
test-languages: test-images
	DREDD_DOCKER_LANG_TEST=1 DREDD_DOCKER_PREWARM=1 $(GO) test -run AllLanguagesDocker ./... -timeout 60m -v
