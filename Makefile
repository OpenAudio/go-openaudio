NETWORK ?= stage
WRAPPER_TAG ?= default
# One of patch, minor, or major
UPGRADE_TYPE ?= patch

PROD_STATE_SYNC_RPCS ?= https://creatornode.audius.co,https://creatornode2.audius.co

GIT_SHA := $(shell git rev-parse HEAD)
VERSION_LDFLAG := -X github.com/OpenAudio/go-openaudio/pkg/core/config.Version=$(GIT_SHA)

.DEFAULT_GOAL := up

###### SQL
CORE_SQL_SRCS := $(shell find pkg/core/db/sql -type f -name '*.sql') pkg/core/db/sqlc.yaml
CORE_SQL_ARTIFACTS := $(wildcard pkg/core/db/*.sql.go)

ETH_SQL_SRCS := $(shell find pkg/eth/db/sql -type f -name '*.sql') pkg/eth/db/sqlc.yaml
ETH_SQL_ARTIFACTS := $(wildcard pkg/eth/db/*.sql.go)

ETL_SQL_SRCS := $(shell find pkg/etl/db/sql -type f -name '*.sql') pkg/etl/db/sqlc.yaml
ETL_SQL_ARTIFACTS := $(wildcard pkg/etl/db/*.sql.go)

SQL_ARTIFACTS := $(CORE_SQL_ARTIFACTS) $(ETH_SQL_ARTIFACTS) $(ETL_SQL_ARTIFACTS)

###### PROTO
PROTO_SRCS := $(shell find proto -type f -name '*.proto')
PROTO_ARTIFACTS := $(shell find pkg/api -type f -name '*.pb.go')

###### TEMPL
TEMPL_SRCS := $(shell find pkg/core/console -type f -name "*.templ")
TEMPL_ARTIFACTS := $(shell find pkg/core/console -type f -name "*_templ.go")

EXPLORER_TEMPL_SRCS := $(shell find pkg/console/templates -type f -name "*.templ")
EXPLORER_TEMPL_ARTIFACTS := $(shell find pkg/console/templates -type f -name "*_templ.go")

###### CSS
EXPLORER_CSS_INPUT := pkg/console/assets/input.css
EXPLORER_CSS_OUTPUT := pkg/console/assets/css/output.css

###### CODE
JSON_SRCS := $(wildcard pkg/core/config/genesis/*.json)
JS_SRCS := $(shell find pkg/core -type f -name '*.js')
GO_SRCS := $(shell find pkg cmd -type f -name '*.go')

BUILD_SRCS := $(GO_SRCS) $(JS_SRCS) $(JSON_SRCS) go.mod go.sum

bin/openaudio-native: $(BUILD_SRCS)
	@echo "Building openaudio for local platform and architecture..."
	@bash scripts/build-openaudio.sh $@

bin/openaudio-x86_64-linux: $(BUILD_SRCS)
	@echo "Building x86 openaudio for linux..."
	@bash scripts/build-openaudio.sh $@ amd64 linux

bin/openaudio-arm64-linux: $(BUILD_SRCS)
	@echo "Building arm openaudio for linux..."
	@bash scripts/build-openaudio.sh $@ arm64 linux

.PHONY: ignore-code-gen
ignore-code-gen:
	@echo "Warning: not regenerating .go files from sql, templ, proto, etc. Using existing artifacts instead."
	@touch $(SQL_ARTIFACTS) $(TEMPL_ARTIFACTS) $(PROTO_ARTIFACTS) go.mod

.PHONY: build-push-cpp
docker-push-cpp:
	docker buildx build --platform linux/amd64,linux/arm64 --push -t audius/cpp:bookworm -f ./cmd/openaudio/Dockerfile.deps ./

.PHONY: clean
clean:
	rm -f bin/*
	rm -f pkg/eth/contracts/gen/*.go

.PHONY: init-hooks
init-hooks:
	@gookme init --types pre-commit,pre-push || echo "Gookme init failed, check if it's installed (https://lmaxence.github.io/gookme)"

.PHONY: install-deps
install-deps:
	go install -v github.com/bufbuild/buf/cmd/buf@latest
	go install -v github.com/sqlc-dev/sqlc/cmd/sqlc@latest
	go install -v github.com/cortesi/modd/cmd/modd@latest
	go install -v github.com/a-h/templ/cmd/templ@latest
	go install -v github.com/ethereum/go-ethereum/cmd/abigen@latest
	go install -v github.com/golangci/golangci-lint/cmd/golangci-lint@latest
	go install -v github.com/templui/templui/cmd/templui@latest

.PHONY: lint
lint:
	golangci-lint run

.PHONY: lint-fix
lint-fix:
	golangci-lint run --fix

go.sum: go.mod
go.mod: $(GO_SRCS)
	go mod tidy
	@touch go.mod # in case there's nothing to tidy

.PHONY: gen
gen: regen-templ regen-proto regen-sql

.PHONY: regen-templ
regen-templ:
	@echo Regenerating console templ code...
	@cd pkg/core/console && templ generate -log-level error
	@echo Regenerating explorer templ code...
	@cd pkg/console/templates && templ generate -log-level error
	@$(MAKE) regen-css

.PHONY: regen-css
regen-css: $(EXPLORER_CSS_OUTPUT)

$(EXPLORER_CSS_OUTPUT): $(EXPLORER_CSS_INPUT) $(EXPLORER_TEMPL_SRCS)
	@echo Regenerating explorer CSS
	@cd pkg/console/assets && \
		(npm list @tailwindcss/postcss > /dev/null 2>&1 || npm install --no-save @tailwindcss/postcss postcss-cli > /dev/null 2>&1) && \
		npx postcss input.css -o css/output.css --minify || \
		(echo "Error: Failed to regenerate CSS. You may need to run manually:" && \
		 echo "  cd pkg/console/assets && npm install && npx postcss input.css -o css/output.css" && exit 1)
	@touch pkg/console/templates/layouts/frame.templ 2>/dev/null || touch pkg/console/console.go 2>/dev/null || true

.PHONY: regen-proto
regen-proto: $(PROTO_ARTIFACTS)

$(PROTO_ARTIFACTS): $(PROTO_SRCS)
	@echo Regenerating protobuf code
	buf --version
	buf generate

.PHONY: regen-sql
regen-sql: regen-core-sql regen-eth-sql regen-etl-sql

.PHONY: regen-core-sql
regen-core-sql: $(CORE_SQL_ARTIFACTS)

$(CORE_SQL_ARTIFACTS): $(CORE_SQL_SRCS)
	@echo Regenerating sql code
	cd pkg/core/db && sqlc generate

.PHONY: regen-eth-sql
regen-eth-sql: $(ETH_SQL_ARTIFACTS)

$(ETH_SQL_ARTIFACTS): $(ETH_SQL_SRCS)
	@echo Regenerating eth sql code
	cd pkg/eth/db && sqlc generate

.PHONY: regen-etl-sql
regen-etl-sql: $(ETL_SQL_ARTIFACTS)

$(ETL_SQL_ARTIFACTS): $(ETL_SQL_SRCS)
	@echo Regenerating etl sql code
	cd pkg/etl/db && sqlc generate

.PHONY: regen-contracts
regen-contracts:
	@echo Regenerating contracts
	cd pkg/eth/contracts && sh -c "./generate_contract.sh"

.PHONY: docker-harness docker-dev
docker-harness: docker-dev bin/openaudio-arm64-linux
	docker build \
		--target harness \
		--build-arg GIT_SHA=$(GIT_SHA) \
		--build-arg PREBUILT_BINARY=bin/openaudio-arm64-linux \
		-t openaudio/go-openaudio:harness \
		-f ./cmd/openaudio/Dockerfile \
		./

docker-dev: bin/openaudio-arm64-linux
	docker build \
		--target dev \
		--build-arg GIT_SHA=$(GIT_SHA) \
		--build-arg PREBUILT_BINARY=bin/openaudio-arm64-linux \
		-t openaudio/go-openaudio:dev \
		-f ./cmd/openaudio/Dockerfile \
		./


.PHONY: up down
up: down docker-dev
	@docker compose \
		--file='dev/docker-compose.yml' \
		--project-name='dev' \
		--project-directory='./' \
		--profile=openaudio-dev \
		up -d

.PHONY: run-prod
run-prod: docker-dev
	@docker rm -f openaudio-prod-ss 2>/dev/null || true
	@mkdir -p "$$(pwd)/tmp/prod-statesync-data"
	@docker run --rm -it \
		--name openaudio-prod-ss \
		-p 8080:80 -p 8443:443 \
		-v "$$(pwd)/tmp/prod-statesync-data:/data" \
		-v "$$(pwd)/cmd:/app/cmd" \
		-v "$$(pwd)/pkg:/app/pkg" \
		-v "$$(pwd)/go.mod:/app/go.mod" \
		-v "$$(pwd)/go.sum:/app/go.sum" \
		-e OPENAUDIO_HOT_RELOAD=true \
		-e NETWORK=prod \
		-e OPENAUDIO_ETL_ENABLED=true \
		-e OPENAUDIO_STORAGE_ENABLED=false \
		-e OPENAUDIO_TLS_SELF_SIGNED=true \
		-e stateSyncEnable=true \
		-e nodeEndpoint=https://node.oap \
		-e stateSyncRPCServers="$(PROD_STATE_SYNC_RPCS)" \
		openaudio/go-openaudio:dev

.PHONY: ss
ss:
	@docker compose \
		--file='dev/docker-compose.yml' \
		--project-name='dev' \
		--project-directory='./' \
		--profile=state-sync-tests \
		up -d

.PHONY: ss-down
ss-down:
	@docker compose \
		--file='dev/docker-compose.yml' \
		--project-name='dev' \
		--project-directory='./' \
		--profile=state-sync-tests \
		down -v

down: ss-down
	@docker compose \
		--file='dev/docker-compose.yml' \
		--project-name='dev' \
		--project-directory='./' \
		--profile=openaudio-dev \
		down -v
	rm -rf tmp/oap*

.PHONY: test
test: test-mediorum test-integration test-unit

.PHONY: test-state-sync
test-state-sync:
	@bash scripts/test-state-sync.sh

.PHONY: test-unit
test-unit:
	@if [ -z "$(OPENAUDIO_CI)" ]; then \
		$(MAKE) docker-harness; \
	fi
	@docker compose \
		--file='dev/docker-compose.yml' \
		--project-name='test' \
		--project-directory='./' \
		--profile=unittests \
		run $(TTY_FLAG) --rm test-unittests

.PHONY: test-mediorum
test-mediorum:
	@if [ -z "$(OPENAUDIO_CI)" ]; then \
		$(MAKE) docker-harness; \
	fi
	@docker compose \
		--file='dev/docker-compose.yml' \
		--project-name='test' \
		--project-directory='./' \
		--profile=mediorum-unittests \
		run $(TTY_FLAG) --rm test-mediorum-unittests

.PHONY: test-integration
test-integration:
	@if [ -z "$(OPENAUDIO_CI)" ]; then \
		$(MAKE) docker-harness; \
	fi
	@docker compose \
		--file='dev/docker-compose.yml' \
		--project-name='test' \
		--project-directory='./' \
		--profile=integration-tests \
		run $(TTY_FLAG) --rm test-integration \
		|| (echo "Tests failed, but containers left running. Use 'make test-down' to cleanup." && false)
	@echo 'Tests complete. Spinning down containers...'
	@docker compose \
		--file='dev/docker-compose.yml' \
		--project-name='test' \
		--project-directory='./' \
		--profile=integration-tests \
		down -v

.PHONY: test-down
test-down:
	@docker compose \
		--file='dev/docker-compose.yml' \
		--project-name='test' \
		--project-directory='./' \
		--profile=integration-tests \
		--profile=mediorum-unittests \
		--profile=unittests \
		down -v

.PHONY: examples/upload
examples/upload:
	cd examples/upload && go run .

.PHONY: examples/upload-ddex
examples/upload-ddex:
	cd examples/upload-ddex && go run .

.PHONY: examples/programmable-distribution
examples/programmable-distribution:
	cd examples/programmable-distribution && go run .

.PHONY: examples/programmable-distribution-ddex
examples/programmable-distribution-ddex:
	cd examples/programmable-distribution-ddex && go run .
