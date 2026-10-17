VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse HEAD 2>/dev/null)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)

.PHONY: build test lint vuln check check-web run docker clean

build: ## Build the binary
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o ocap-stats ./cmd/ocap-stats

test: ## Run tests with the race detector
	go test -race ./...

lint: ## Run go vet and staticcheck
	go vet ./...
	go run honnef.co/go/tools/cmd/staticcheck@latest ./...

vuln: ## Check dependencies for known vulnerabilities
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...

check: ## Everything CI checks, plus the web checks (needs Node.js for check-web)
	@test -z "$$(gofmt -l .)" || (echo "gofmt needed:"; gofmt -l .; exit 1)
	go vet ./...
	go run honnef.co/go/tools/cmd/staticcheck@latest ./...
	go test -race ./...
	$(MAKE) check-web

check-web: ## JS syntax and HTML/accessibility validation of the pages
	for f in web/static/*.js web/static/admin/*.js; do node --check $$f || exit 1; done
	npx --yes html-validate@9 web/static/index.html web/static/admin/index.html

run: build ## Run locally (needs OCAPSTATS_OCAP_URL)
	./ocap-stats

docker: ## Build the Docker image
	docker build --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) --build-arg DATE=$(DATE) -t ocap-statistics:$(VERSION) .

clean:
	rm -f ocap-stats ocap-stats.exe
