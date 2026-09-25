PLUGIN_DIR := $(HOME)/.fluxcd/plugins
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/Stratio/flux-stratio/internal/cli.version=$(VERSION)

.PHONY: build install vet lint fmt-check test clean package install-package deploy change-version

build:
	GOWORK=off go build -ldflags "$(LDFLAGS)" -o bin/flux-stratio ./cmd/flux-stratio

install: build
	mkdir -p $(PLUGIN_DIR)
	cp bin/flux-stratio $(PLUGIN_DIR)/flux-stratio

vet:
	GOWORK=off go vet ./...

lint:
	GOWORK=off golangci-lint run ./...

fmt-check:
	@fmtout=$$(gofmt -l .); \
	if [ -n "$$fmtout" ]; then \
		echo "gofmt needs to be run on:"; echo "$$fmtout"; exit 1; \
	fi

test:
	GOWORK=off go test ./...

clean:
	rm -rf bin/flux-stratio bin/flux-stratio-*.tar.gz

package:
	make build && bin/package.sh $(version)

install-package: package
	bin/install-package.sh $(version)

deploy:
	bin/deploy.sh $(version)

change-version:
	bin/change-version.sh $(version)
