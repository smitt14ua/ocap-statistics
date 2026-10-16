VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse HEAD 2>/dev/null)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)

.PHONY: build test lint vuln run docker clean

build: ## Build the binary
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o ocap-stats ./cmd/ocap-stats

test: ## Run tests with the race detector
	go test -race ./...

lint: ## Run go vet and staticcheck
	go vet ./...
	go run honnef.co/go/tools/cmd/staticcheck@latest ./...

vuln: ## Check dependencies for known vulnerabilities
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...

run: build ## Run locally (needs OCAPSTATS_OCAP_URL)
	./ocap-stats

docker: ## Build the Docker image
	docker build --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) --build-arg DATE=$(DATE) -t ocap-statistics:$(VERSION) .

clean:
	rm -f ocap-stats ocap-stats.exe
