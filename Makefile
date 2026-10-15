GO_BUILD_FLAGS ?= -trimpath

.PHONY: build test vet studio-install studio-dev studio-build studio-lint

build:
	go build $(GO_BUILD_FLAGS) -o bin/tracelet ./cmd/tracelet

test:
	go test ./...

vet:
	go vet ./...

studio-install:
	cd studio && npm ci

studio-dev:
	cd studio && npm run dev

studio-build:
	cd studio && npm run build

studio-lint:
	cd studio && npm run lint
