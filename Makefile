# RayBox build shortcuts. Release builds live in tools/ and .github/workflows/.
NAME = raybox
TAGS ?= $(shell cat release/DEFAULT_BUILD_TAGS_OTHERS)
VERSION = $(shell go run ./cmd/internal/read_tag)
LDFLAGS_SHARED = $(shell cat release/LDFLAGS)
PARAMS = -v -trimpath -ldflags "-X 'github.com/roseforljh/RayBox-rx/constant.Version=$(VERSION)' $(LDFLAGS_SHARED) -s -w -buildid="
MAIN_PARAMS = $(PARAMS) -tags "$(TAGS)"
MAIN = ./cmd/raybox
PREFIX ?= $(shell go env GOPATH)

.PHONY: build server install generate_completions schema fmt lint proto proto_check lib_android lib_apple lib_install clean

build:
	export GOTOOLCHAIN=local && go build $(MAIN_PARAMS) -o $(NAME) $(MAIN)

server:
	export GOTOOLCHAIN=local && go build $(PARAMS) -o $(NAME)-server ./cmd/raybox-server

install:
	go build -o $(PREFIX)/bin/$(NAME) $(MAIN_PARAMS) $(MAIN)

generate_completions:
	go run -v --tags "$(TAGS),generate_completions" $(MAIN)

schema:
	go run -ldflags "$(LDFLAGS_SHARED)" --tags "$(TAGS)" $(MAIN) schema -o docs/schema.json

fmt:
	@golangci-lint fmt

lint:
	GOOS=linux golangci-lint run ./...
	GOOS=android golangci-lint run ./...
	GOOS=windows golangci-lint run ./...
	GOOS=darwin golangci-lint run ./...

PROTO_DIRS = internal/protocol daemon experimental/boxdd experimental/v2rayapi transport/v2raygrpc

proto:
	for dir in $(PROTO_DIRS); do go run ./tools/vprotogen -pwd $$dir || exit 1; done

proto_check:
	for dir in $(PROTO_DIRS); do go run ./tools/vprotogen -pwd $$dir -mode check || exit 1; done

lib_android:
	pwsh ./tools/build-libbox.ps1

lib_apple:
	go run ./cmd/internal/build_libbox -target apple

lib_install:
	go install -v github.com/sagernet/gomobile/cmd/gomobile@v0.1.13
	go install -v github.com/sagernet/gomobile/cmd/gobind@v0.1.13

clean:
	rm -rf bin dist $(NAME) $(NAME)-server
