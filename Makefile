GO_BUILD_FLAGS ?= -trimpath

.PHONY: build build-go test vet studio-install studio-dev studio-build studio-lint

# build compiles Tracelet Studio and embeds it in bin/tracelet.
build: studio-build build-go

# build-go skips the UI (Studio then shows a "not built" notice). No Node needed.
build-go:
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
	cd studio && (test -d node_modules || npm ci) && npm run build

studio-lint:
	cd studio && npm run lint
