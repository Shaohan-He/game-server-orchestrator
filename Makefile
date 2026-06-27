.PHONY: all build test lint vet generate clean docker-build docker-push deploy install-crds dev-up dev-down

# Image registry and tag.
# Default image registry. Override REGISTRY for another registry if needed.
REGISTRY    ?= ghcr.io/shaohan-he
TAG         ?= v0.1.0
CONTROLLER_IMG := $(REGISTRY)/game-fleet-director-controller:$(TAG)
APISERVER_IMG  := $(REGISTRY)/game-fleet-director-apiserver:$(TAG)

# Go 代理加速（国内必配）
export GOPROXY ?= https://goproxy.cn,direct

# Go build flags.
LDFLAGS := -s -w -X main.version=$(TAG)

all: generate lint test build

## build: compile controller and apiserver binaries.
build:
	go build -ldflags "$(LDFLAGS)" -o bin/controller ./cmd/controller
	go build -ldflags "$(LDFLAGS)" -o bin/apiserver  ./cmd/apiserver

## test: run unit tests.
test:
	go test ./pkg/... -v -count=1 -timeout 60s

## test-integration: run integration tests (requires envtest).
test-integration:
	go test ./tests/integration/... -v -count=1 -timeout 120s

## test-e2e: run end-to-end tests (requires kind cluster).
test-e2e:
	go test ./tests/e2e/... -v -count=1 -timeout 300s

## lint: run golangci-lint.
lint:
	golangci-lint run ./...

## vet: run go vet.
vet:
	go vet ./...

## generate: regenerate CRD deepcopy and client code.
generate:
	go generate ./...

## clean: remove build artifacts.
clean:
	rm -rf bin/

## docker-build: build Docker images.
docker-build: build
	docker build -t $(CONTROLLER_IMG) --target controller .
	docker build -t $(APISERVER_IMG)  --target apiserver .

## docker-push: push Docker images to registry.
docker-push:
	docker push $(CONTROLLER_IMG)
	docker push $(APISERVER_IMG)

## deploy: deploy to current kubectl context.
deploy:
	kubectl apply -k deploy/

## install-crds: install CRDs only.
install-crds:
	kubectl apply -f config/crds/

## dev-up: create kind cluster and deploy full stack.
dev-up:
	kind create cluster --name game-fleet-dev || true
	kubectl apply -f config/crds/
	kubectl apply -k deploy/
	kubectl wait --for=condition=available deploy/game-fleet-director-controller -n game-fleet-system --timeout=60s

## dev-down: destroy kind cluster.
dev-down:
	kind delete cluster --name game-fleet-dev

## run-controller: run controller locally (requires kubeconfig).
run-controller:
	go run ./cmd/controller --metrics-addr=:8080 --leader-elect=false

## run-apiserver: run API server locally (requires kubeconfig).
run-apiserver:
	go run ./cmd/apiserver --api-addr=:8443 --rate-limit=100
