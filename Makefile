CONTROLLER_GEN ?= controller-gen
IMG ?= clusterview:latest
PLATFORM ?= linux/amd64
ENVTEST_K8S_VERSION ?= 1.31.x

.PHONY: generate
generate:
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./api/..."
	$(CONTROLLER_GEN) rbac:roleName=clusterview-role crd paths="./..." \
		output:crd:artifacts:config=config/crd/bases \
		output:rbac:artifacts:config=config/rbac
	mkdir -p deploy/helm/clusterview/crds
	cp config/crd/bases/clusterview.io_clusterviews.yaml deploy/helm/clusterview/crds/

.PHONY: fmt vet build test test-envtest
fmt:
	go fmt ./...
vet:
	go vet ./...
build:
	CGO_ENABLED=0 go build -o bin/manager ./cmd/manager
test:
	go test ./...
# Controller + end-to-end API/RBAC tests against a real kube-apiserver/etcd.
test-envtest:
	go install sigs.k8s.io/controller-runtime/tools/setup-envtest@release-0.19
	KUBEBUILDER_ASSETS="$$(setup-envtest use $(ENVTEST_K8S_VERSION) -p path)" go test ./... -v

.PHONY: docker-build docker-push helm-lint
# Cluster nodes are linux/amd64; cross-build so this works from an arm64 Mac.
docker-build:
	docker buildx build --platform $(PLATFORM) -t $(IMG) --load .
docker-push:
	docker buildx build --platform $(PLATFORM) -t $(IMG) --push .
helm-lint:
	helm lint deploy/helm/clusterview
	helm template clusterview deploy/helm/clusterview >/dev/null
