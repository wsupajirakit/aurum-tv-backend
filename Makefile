# Everyday tasks. CPU is capped (nice + -p) so a laptop stays responsive.
GO      ?= go
JOBS    ?= 6
NICE    ?= nice -n 10
GOBIN   := $(shell $(GO) env GOPATH)/bin
LDFLAGS := -s -w

.PHONY: run build test race cover vet lint vuln check tidy docker

run: ## run the API with .env
	$(GO) run ./cmd/aurum-api

build: ## static binaries into bin/
	CGO_ENABLED=0 $(NICE) $(GO) build -p $(JOBS) -trimpath -ldflags="$(LDFLAGS)" -o bin/ ./cmd/...

test:
	$(NICE) $(GO) test -p $(JOBS) ./...

race:
	$(NICE) $(GO) test -race -p $(JOBS) -count=1 ./...

cover:
	$(NICE) $(GO) test -p $(JOBS) -coverpkg=./internal/... -coverprofile=coverage.out ./... && $(GO) tool cover -func=coverage.out | tail -1

vet:
	$(NICE) $(GO) vet ./...

lint: ## staticcheck (go install honnef.co/go/tools/cmd/staticcheck@latest)
	$(NICE) $(GOBIN)/staticcheck ./...

vuln: ## govulncheck (go install golang.org/x/vuln/cmd/govulncheck@latest)
	$(NICE) $(GOBIN)/govulncheck ./...

check: vet lint vuln race ## everything CI runs

tidy:
	$(GO) mod tidy

docker:
	docker build -t aurum-tv-api .
