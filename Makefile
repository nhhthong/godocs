.PHONY: run test smoke cover vet lint build ci
run:   ; go run ./cmd/api
test:  ; go test -race -cover ./...
smoke: ; go test -race -run TestSmoke -v ./cmd/api          # Executes realistic end-to-end HTTP integration smoke tests
cover: ; go test -coverpkg=./... -cover ./...               # Generates cross-package aggregate test coverage
vet:   ; go vet ./...
lint:  ; golangci-lint run ./...                            # Runs static analysis linters (requires .golangci.yml)
build: ; CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/api ./cmd/api
ci:    vet test                                             # Quality gate for pre-push git hooks and CI pipelines
