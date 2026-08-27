VERSION ?= dev
IMAGE   ?= raccooncore/blastdoor
LDFLAGS := -s -w -X github.com/raccoon-core/blastdoor/internal/cli.Version=$(VERSION)

.PHONY: check
check: fmt-check vet test ## Everything CI runs

.PHONY: build
build: ## Build ./bin/blastdoor
	go build -ldflags="$(LDFLAGS)" -o bin/blastdoor ./cmd/blastdoor

.PHONY: test
test: ## Run the tests
	go test -race ./...

.PHONY: vet
vet:
	go vet ./...

.PHONY: fmt
fmt: ## Format the code
	gofmt -w .

.PHONY: fmt-check
fmt-check:
	@unformatted=$$(gofmt -l .); \
	if [ -n "$$unformatted" ]; then \
		echo "These files need gofmt:"; echo "$$unformatted"; exit 1; \
	fi

.PHONY: image
image: ## Build the docker image
	docker build --build-arg VERSION=$(VERSION) -t $(IMAGE):$(VERSION) .

.PHONY: examples
examples: build ## Score every example plan
	@for plan in examples/plans/*.json; do \
		echo "=== $$plan ==="; \
		./bin/blastdoor eval --plan "$$plan" --policy examples/policies --out-dir /tmp/blastdoor-examples; \
	done

DOCS_VENV := .venv-docs
DOCS_BIN  := $(DOCS_VENV)/bin

# Pinned in docs/requirements.txt so this is the site CI publishes.
$(DOCS_VENV): docs/requirements.txt
	python3 -m venv $(DOCS_VENV)
	$(DOCS_BIN)/pip install -q --upgrade pip
	$(DOCS_BIN)/pip install -q -r docs/requirements.txt
	@touch $(DOCS_VENV)

.PHONY: docs-serve
docs-serve: $(DOCS_VENV) ## Preview the docs site on localhost:8000
	$(DOCS_BIN)/mkdocs serve

.PHONY: docs-build
docs-build: $(DOCS_VENV) ## Build the docs site the way CI does
	$(DOCS_BIN)/mkdocs build --strict

.PHONY: release-check
release-check: ## Validate the release config without publishing
	goreleaser check
	goreleaser build --snapshot --clean --single-target
	npx --yes semantic-release@25 --dry-run --no-ci

.PHONY: clean
clean:
	rm -rf bin dist .blastdoor site $(DOCS_VENV)

.PHONY: help
help: ## List targets
	@grep -hE '^[a-z-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "%-12s %s\n", $$1, $$2}'
