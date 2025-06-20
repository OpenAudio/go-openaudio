NETWORK ?= stage
WRAPPER_TAG ?= default
# One of patch, minor, or major
UPGRADE_TYPE ?= patch

GIT_SHA := $(shell git rev-parse HEAD)

SQL_SRCS := $(shell find pkg/core/db/sql -type f -name '*.sql') pkg/core/db/sqlc.yaml
SQL_ARTIFACTS := $(wildcard pkg/core/db/*.sql.go)

ETL_SQL_SRCS := $(shell find pkg/etl/db/sql -type f -name '*.sql') pkg/etl/db/sqlc.yaml
ETL_SQL_ARTIFACTS := $(wildcard pkg/etl/db/*.sql.go)

PROTO_SRCS := $(shell find proto -type f -name '*.proto')
PROTO_ARTIFACTS := $(shell find pkg/api -type f -name '*.pb.go')

TEMPL_SRCS := $(shell find pkg/core/console -type f -name "*.templ")
TEMPL_ARTIFACTS := $(shell find pkg/core/console -type f -name "*_templ.go")

CONTRACT_SRCS := contracts/ProportionalRewards.sol
CONTRACT_ARTIFACTS := contracts/build/ProportionalRewards.abi \
                     contracts/build/ProportionalRewards.bin

CONTRACT_GO_SRCS := $(CONTRACT_ARTIFACTS)
CONTRACT_GO_ARTIFACTS := pkg/core/contracts/gen/proportional_rewards.go

VERSION_LDFLAG := -X github.com/AudiusProject/audius-protocol/core/config.Version=$(GIT_SHA)

JSON_SRCS := $(wildcard pkg/core/config/genesis/*.json)
JS_SRCS := $(shell find pkg/core -type f -name '*.js')
GO_SRCS := $(shell find pkg cmd -type f -name '*.go')

BUILD_SRCS := $(GO_SRCS) $(JS_SRCS) $(JSON_SRCS) go.mod go.sum

bin/audiusd-native: $(BUILD_SRCS)
	@echo "Building audiusd for local platform and architecture..."
	@bash scripts/build-audiusd.sh $@

bin/audiusd-x86_64-linux: $(BUILD_SRCS)
	@echo "Building x86 audiusd for linux..."
	@bash scripts/build-audiusd.sh $@ amd64 linux

bin/audiusd-arm64-linux: $(BUILD_SRCS)
	@echo "Building arm audiusd for linux..."
	@bash scripts/build-audiusd.sh $@ arm64 linux

bin/audius-ctl-native: $(BUILD_SRCS)
	@echo "Building audius-ctl for local platform and architecture..."
	@bash scripts/build-audius-ctl.sh $@

bin/audius-ctl-arm64-linux: $(BUILD_SRCS)
	@echo "Building arm audius-ctl for linux..."
	@bash scripts/build-audius-ctl.sh $@ arm64 linux

bin/audius-ctl-x86_64-linux: $(BUILD_SRCS)
	@echo "Building x86 audius-ctl for linux..."
	@bash scripts/build-audius-ctl.sh $@ amd64 linux

bin/audius-ctl-arm64-darwin: $(BUILD_SRCS)
	@echo "Building macos arm audius-ctl..."
	@bash scripts/build-audius-ctl.sh $@ arm64 darwin

bin/audius-ctl-x86_64-darwin: $(BUILD_SRCS)
	@echo "Building macos x86 audius-ctl..."
	@bash scripts/build-audius-ctl.sh $@ amd64 darwin

bin/aupl: $(BUILD_SRCS)
	@echo "Building aupl for native platform and architecture..."
	@CGO_ENABLED=0 go build -o $@ ./cmd/aupl

.PHONY: release-audius-ctl audius-ctl-production-build
release-audius-ctl:
	bash scripts/release-audius-ctl.sh

audius-ctl-production-build: clean ignore-code-gen bin/audius-ctl-arm64-linux bin/audius-ctl-x86_64-linux bin/audius-ctl-arm64-darwin bin/audius-ctl-x86_64-darwin

.PHONY: ignore-code-gen
ignore-code-gen:
	@echo "Warning: not regenerating .go files from sql, templ, proto, etc. Using existing artifacts instead."
	@touch $(SQL_ARTIFACTS) $(TEMPL_ARTIFACTS) $(PROTO_ARTIFACTS) go.mod

.PHONY: build-wrapper-local build-push-wrapper
docker-wrapper-local:
	@echo "Building Docker image for local platform..."
	docker buildx build --load -t audius/audius-d:$(WRAPPER_TAG) pkg/orchestration

docker-push-wrapper:
	@echo "Building and pushing Docker images for all platforms..."
	docker buildx build --platform linux/amd64,linux/arm64 --push -t audius/audius-d:$(WRAPPER_TAG) pkg/orchestration

.PHONY: build-push-cpp
docker-push-cpp:
	docker buildx build --platform linux/amd64,linux/arm64 --push -t audius/cpp:bookworm -f ./cmd/audiusd/Dockerfile.deps ./

.PHONY: install uninstall
install:
	@bash scripts/install-audius-ctl.sh local

uninstall:
	@bash scripts/uninstall-audius-ctl.sh

.PHONY: clean
clean:
	rm -f bin/*
	rm -f contracts/build/*.abi contracts/build/*.bin
	rm -f pkg/core/contracts/gen/*.go

.PHONY: install-deps install-go-deps
install-deps: install-go-deps
	@brew install protobuf
	@brew install crane
	@brew install bufbuild/buf/buf
	@brew install solidity
	@gookme init --types pre-commit,pre-push || echo "Gookme init failed, check if it's installed (https://lmaxence.github.io/gookme)"

install-go-deps:
	go install -v github.com/sqlc-dev/sqlc/cmd/sqlc@latest
	go install -v google.golang.org/protobuf/cmd/protoc-gen-go@latest
	go install -v google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
	go install -v github.com/cortesi/modd/cmd/modd@latest
	go install -v github.com/a-h/templ/cmd/templ@latest
	go install -v github.com/ethereum/go-ethereum/cmd/abigen@latest
	go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest

.PHONY: lint
lint:
	golangci-lint run

.PHONY: lint-fix
lint-fix:
	golangci-lint run --fix

go.sum: go.mod
go.mod: $(GO_SRCS)
	@# dummy go.mod file to speed up tidy times
	@[ -d node_modules ] && touch node_modules/go.mod || true
	go mod tidy
	@touch go.mod # in case there's nothing to tidy

.PHONY: gen
gen: regen-templ regen-proto regen-sql regen-etl-sql gen-contracts

.PHONY: regen-templ
regen-templ: $(TEMPL_ARTIFACTS)
$(TEMPL_ARTIFACTS): $(TEMPL_SRCS)
	@echo Regenerating templ code
	cd pkg/core/console && templ generate -log-level error

.PHONY: regen-proto
regen-proto: $(PROTO_ARTIFACTS)
$(PROTO_ARTIFACTS): $(PROTO_SRCS)
	@echo Regenerating protobuf code
	buf --version
	buf generate

.PHONY: regen-sql
regen-sql: $(SQL_ARTIFACTS)
$(SQL_ARTIFACTS): $(SQL_SRCS)
	@echo Regenerating sql code
	cd pkg/core/db && sqlc generate

.PHONY: regen-etl-sql
regen-etl-sql: $(ETL_SQL_ARTIFACTS)
$(ETL_SQL_ARTIFACTS): $(ETL_SQL_SRCS)
	@echo Regenerating etl sql code
	cd pkg/etl/db && sqlc generate

.PHONY: regen-contracts
regen-contracts:
	@echo Regenerating contracts
	cd pkg/core && sh -c "./generate_contract.sh"

.PHONY: gen-contracts
gen-contracts: $(CONTRACT_GO_ARTIFACTS)
$(CONTRACT_GO_ARTIFACTS): $(CONTRACT_SRCS)
	@echo "Building and generating contracts"
	@mkdir -p contracts/build
	@mkdir -p pkg/core/contracts/gen
	solc --abi --bin --overwrite -o contracts/build contracts/ProportionalRewards.sol
	abigen \
		--abi contracts/build/ProportionalRewards.abi \
		--bin contracts/build/ProportionalRewards.bin \
		--pkg gen \
		--type ProportionalRewards \
		--out pkg/core/contracts/gen/proportional_rewards.go


.PHONY: docker-test docker-dev docker-local
docker-test:
	DOCKER_DEFAULT_PLATFORM=linux/arm64 docker build --target test --build-arg GIT_SHA=$(GIT_SHA) -t audius/audiusd:test -f ./cmd/audiusd/Dockerfile ./

docker-dev:
	DOCKER_DEFAULT_PLATFORM=linux/arm64 docker build --target dev --build-arg GIT_SHA=$(GIT_SHA) -t audius/audiusd:dev -f ./cmd/audiusd/Dockerfile ./

docker-local:
	DOCKER_DEFAULT_PLATFORM=linux/arm64 docker build --target prod --build-arg GIT_SHA=$(GIT_SHA) -t audius/audiusd:local -f ./cmd/audiusd/Dockerfile ./

.PHONY: up down
up: down docker-dev docker-test
	@docker compose \
		--file='dev/docker-compose.yml' \
		--project-name='dev' \
		--project-directory='./' \
		--profile=audiusd-dev \
		up -d

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
		--profile=audiusd-dev \
		down -v

.PHONY: test
test: mediorum-test core-test

.PHONY: mediorum-test
mediorum-test:
	@if [ -z "$(AUDIUSD_TEST_IMAGE)" ]; then \
		make docker-test; \
	fi
	@docker compose \
		--file='dev/docker-compose.yml' \
		--project-name='test' \
		--project-directory='./' \
		--profile=mediorum-unittests \
		run $(TTY_FLAG) --rm test-mediorum-unittests
	@echo 'Tests successful. Spinning down containers...'
	@docker compose \
    	--file='dev/docker-compose.yml' \
        --project-name='test' \
        --project-directory='./' \
		--profile=mediorum-unittests \
        down -v

.PHONY: core-test
core-test:
	@if [ -z "$(AUDIUSD_TEST_IMAGE)" ]; then \
		make docker-test; \
	fi
	docker compose \
		--file='dev/docker-compose.yml' \
		--project-name='test' \
		--project-directory='./' \
		--profile=core-tests \
		run $(TTY_FLAG) --rm test-core
	@echo 'Tests complete. Spinning down containers...'
	@docker compose \
		--file='dev/docker-compose.yml' \
		--project-name='test' \
		--project-directory='./' \
		--profile=core-tests \
		down -v
