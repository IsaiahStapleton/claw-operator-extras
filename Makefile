# Image URL to use for building and pushing the deployer image.
DEPLOYER_IMG ?= quay.io/redhat-et/claw-deployer-ui:latest
# Image URL to use for building and pushing the agent console image.
CONSOLE_IMG ?= quay.io/redhat-et/agent-console:latest
CONSOLE_LOCAL_ADDR ?= :18081
# Point at a directory laid out as <dir>/<agent>/sessions/*.jsonl
CONSOLE_LOCAL_DATA_DIR ?= ./testdata/agents
PLATFORM ?= linux/amd64
DEPLOYER_LOCAL_ADDR ?= :18080
DEPLOYER_LOCAL_USER ?= $(USER)
DEPLOYER_LOCAL_KUBE_API_SERVER ?= http://127.0.0.1:9

# CONTAINER_TOOL defines the container tool to be used for building images.
CONTAINER_TOOL ?= podman

# Setting SHELL to bash allows bash commands to be executed by recipes.
SHELL = /usr/bin/env bash -o pipefail

.PHONY: test
test:
	go test ./...

.PHONY: deployer-build
deployer-build: ## Build the OpenClaw deployer UI image. Override with DEPLOYER_IMG=...
	$(CONTAINER_TOOL) build --platform=$(PLATFORM) -f Containerfile -t $(DEPLOYER_IMG) .

.PHONY: deployer-push
deployer-push: ## Push the OpenClaw deployer UI image.
	$(CONTAINER_TOOL) push $(DEPLOYER_IMG)

.PHONY: deployer-run-local
deployer-run-local: ## Run the OpenClaw deployer UI locally for frontend preview.
	LISTEN_ADDR=$(DEPLOYER_LOCAL_ADDR) \
	KUBE_API_SERVER=$(DEPLOYER_LOCAL_KUBE_API_SERVER) \
	DEVELOPER_BEARER_TOKEN=preview \
	DEVELOPER_USERNAME=$(DEPLOYER_LOCAL_USER) \
	OPENCLAW_DEPLOYER_IMPERSONATE=false \
	go run ./cmd/deployer

.PHONY: console-build
console-build: ## Build the Agent Console image. Override with CONSOLE_IMG=...
	$(CONTAINER_TOOL) build --platform=$(PLATFORM) -f Containerfile.console -t $(CONSOLE_IMG) .

.PHONY: console-push
console-push: ## Push the Agent Console image.
	$(CONTAINER_TOOL) push $(CONSOLE_IMG)

.PHONY: console-run-local
console-run-local: ## Run the Agent Console locally against CONSOLE_LOCAL_DATA_DIR.
	LISTEN_ADDR=$(CONSOLE_LOCAL_ADDR) \
	AGENT_DATA_DIR=$(CONSOLE_LOCAL_DATA_DIR) \
	go run ./cmd/console
