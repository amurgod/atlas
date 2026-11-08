IMAGE_TAG ?= local
CLUSTER ?= atlas
HOST_PORT ?= 8080
KIND_VERSION ?= v0.27.0

.PHONY: tidy test build images compose-up compose-down kind-create kind-create-mac kind-load deploy-local deploy-local-mac undeploy kustomize-local

tidy:
	go mod tidy

test:
	go test ./...

build:
	go build ./services/user ./services/knowledge ./services/planner ./services/notification ./services/llm-gateway ./services/open-webui-engine

images:
	docker build --build-arg SERVICE=user -t atlas/user:$(IMAGE_TAG) -f services/Dockerfile .
	docker build --build-arg SERVICE=knowledge -t atlas/knowledge:$(IMAGE_TAG) -f services/Dockerfile .
	docker build --build-arg SERVICE=planner -t atlas/planner:$(IMAGE_TAG) -f services/Dockerfile .
	docker build --build-arg SERVICE=notification -t atlas/notification:$(IMAGE_TAG) -f services/Dockerfile .
	docker build --build-arg SERVICE=llm-gateway -t atlas/llm-gateway:$(IMAGE_TAG) -f services/Dockerfile .
	docker build --build-arg SERVICE=open-webui-engine -t atlas/open-webui-engine:$(IMAGE_TAG) -f services/Dockerfile .
	docker build -t atlas/ui:$(IMAGE_TAG) ui

compose-up:
	docker compose up --build -d
	@echo "Open http://127.0.0.1:$(HOST_PORT)  and chat at http://127.0.0.1:3000"

compose-down:
	docker compose down -v

kind-create:
	@command -v kind >/dev/null || { \
	  echo "Installing kind into ./bin"; \
	  mkdir -p bin; \
	  curl -fsSL -o bin/kind https://kind.sigs.k8s.io/dl/v0.27.0/kind-linux-amd64; \
	  chmod +x bin/kind; \
	}
	$(CURDIR)/bin/kind create cluster --config deploy/kind/cluster.yaml || kind create cluster --config deploy/kind/cluster.yaml
	kubectl apply -f https://raw.githubusercontent.com/kubernetes/ingress-nginx/controller-v1.11.3/deploy/static/provider/kind/deploy.yaml
	kubectl -n ingress-nginx rollout status deployment/ingress-nginx-controller --timeout=180s

# macOS variant of kind-create. Installs kind-darwin-{arm64,amd64} into ./bin
# and replaces a leftover Linux binary that cannot be executed on Darwin.
kind-create-mac:
	@os=$$(uname -s); \
	if [ "$$os" != "Darwin" ]; then \
	  echo "kind-create-mac is for macOS. On Linux use: make kind-create"; \
	  exit 1; \
	fi
	@if ! command -v docker >/dev/null || ! docker info >/dev/null 2>&1; then \
	  echo "Docker is not running. Start Docker Desktop, OrbStack, or Colima, then retry."; \
	  exit 1; \
	fi
	@if ! command -v kubectl >/dev/null; then \
	  echo "kubectl is not on PATH. Install it (brew install kubectl) and retry."; \
	  exit 1; \
	fi
	@mkdir -p bin
	@arch=$$(uname -m); \
	case "$$arch" in \
	  arm64) kind_arch=arm64 ;; \
	  x86_64) kind_arch=amd64 ;; \
	  *) echo "unsupported Mac architecture: $$arch"; exit 1 ;; \
	esac; \
	need_install=0; \
	if [ ! -x bin/kind ] || ! bin/kind version >/dev/null 2>&1; then need_install=1; fi; \
	if [ "$$need_install" = "1" ]; then \
	  echo "Installing kind $(KIND_VERSION) darwin-$$kind_arch into ./bin"; \
	  curl -fsSL -o bin/kind "https://kind.sigs.k8s.io/dl/$(KIND_VERSION)/kind-darwin-$$kind_arch"; \
	  chmod +x bin/kind; \
	fi
	@if $(CURDIR)/bin/kind get clusters 2>/dev/null | grep -qx '$(CLUSTER)'; then \
	  echo "kind cluster $(CLUSTER) already exists"; \
	else \
	  $(CURDIR)/bin/kind create cluster --config deploy/kind/cluster.yaml; \
	fi
	kubectl apply -f https://raw.githubusercontent.com/kubernetes/ingress-nginx/controller-v1.11.3/deploy/static/provider/kind/deploy.yaml
	kubectl -n ingress-nginx rollout status deployment/ingress-nginx-controller --timeout=180s

kind-load: images
	@KIND=$$(command -v kind || echo $(CURDIR)/bin/kind); \
	for img in user knowledge planner notification llm-gateway open-webui-engine ui; do \
	  $$KIND load docker-image atlas/$$img:$(IMAGE_TAG) --name $(CLUSTER); \
	done

kustomize-local:
	kubectl apply -k deploy/k8s/overlays/local

deploy-local: kind-create kind-load kustomize-local
	kubectl -n atlas rollout status deploy/user-service --timeout=180s
	kubectl -n atlas rollout status deploy/gateway --timeout=180s
	kubectl -n atlas rollout status deploy/open-webui-engine --timeout=180s
	@echo "Ingress is on 0.0.0.0:18082. From the Mac open http://<this-host>:18082"

deploy-local-mac: kind-create-mac kind-load kustomize-local
	kubectl -n atlas rollout status deploy/user-service --timeout=180s
	kubectl -n atlas rollout status deploy/gateway --timeout=180s
	kubectl -n atlas rollout status deploy/open-webui-engine --timeout=180s
	@echo "Open http://127.0.0.1:18082"

undeploy:
	kubectl delete -k deploy/k8s/overlays/local --ignore-not-found
