APP      := costbasis
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  := -s -w -X main.version=$(VERSION)
IMAGE    ?= costbasis-br

.PHONY: build test test-integration lint check run docker golden clean

build: ## build bin/costbasis
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(APP) ./cmd/$(APP)

test: ## unit, adapter and golden tests with the race detector
	go test -race ./...

# SQLite runs in-process, so adapter tests already use a real database and no
# test needs external services. The target exists for parity with the other
# repositories and runs anything tagged "integration".
test-integration:
	go test -race -tags integration ./...

lint: ## go vet and gofmt (fails on unformatted files)
	go vet ./...
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

check: lint test ## must pass before any change is done

run: build ## serve the local API with settings from .env
	@set -a; [ -f .env ] && . ./.env; set +a; ./bin/$(APP) serve

docker: ## build the container image
	docker build -f deploy/Dockerfile --build-arg VERSION=$(VERSION) -t $(IMAGE):$(VERSION) .

golden: ## regenerate golden files after an intended behaviour change; review the diff
	go test ./cmd/$(APP) -run Golden -update

clean:
	rm -rf bin
