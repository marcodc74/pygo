# Pygo toolchain build. Requires Go >= 1.22 (no other dependencies).
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64
IMAGE ?= pygo

.PHONY: build test race vet dist guide docker clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o pygo ./cmd/pygo

test: vet
	go test ./...
	go run ./cmd/pygo test examples/

race:
	go test -race ./...

vet:
	gofmt -l . | (! grep .) && go vet ./...

# Static binaries for every OS/arch in dist/.
dist:
	@mkdir -p dist
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; ext=""; [ $$os = windows ] && ext=".exe"; \
		echo "building dist/pygo-$$os-$$arch$$ext"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" \
			-o dist/pygo-$$os-$$arch$$ext ./cmd/pygo || exit 1; \
	done

# Regenerate the compact reference shipped in docs/.
guide:
	go run ./cmd/pygo guide > docs/GUIDE.md

docker:
	docker build -f deploy/Dockerfile -t $(IMAGE):$(VERSION) .

clean:
	rm -rf pygo dist
