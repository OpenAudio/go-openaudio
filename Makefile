NETWORK ?= stage
WRAPPER_TAG ?= default
# One of patch, minor, or major
UPGRADE_TYPE ?= patch

GIT_SHA := $(shell git rev-parse HEAD)
AD_TAG ?= $(GIT_SHA)

SQL_SRCS := $(shell find pkg/core/db/sql -type f -name '*.sql') pkg/core/db/sqlc.yaml
SQL_ARTIFACTS := $(wildcard pkg/core/db/*.sql.go)

PROTO_SRCS := pkg/core/protocol/protocol.proto
PROTO_ARTIFACTS := $(wildcard pkg/core/gen/core_proto/*.pb.go)

TEMPL_SRCS := $(shell find pkg/core/console -type f -name "*.templ")
TEMPL_ARTIFACTS := $(shell find pkg/core/console -type f -name "*_templ.go")

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

# Experimental statusbar feature
bin/audius-ctl-arm64-darwin-experimental: $(BUILD_SRCS)
	@echo "Building macos arm audius-ctl..."
	@GOOS=darwin GOARCH=arm64 go build -tags osx -ldflags -X main.Version="$(shell git rev-parse HEAD)" -o bin/audius-ctl-arm64-darwin-experimental ./cmd/audius-ctl

.PHONY: release-audius-ctl audius-ctl-production-build
release-audius-ctl:
	bash scripts/release-audius-ctl.sh

audius-ctl-production-build: clean ignore-code-gen bin/audius-ctl-arm64-linux bin/audius-ctl-x86_64-linux bin/audius-ctl-arm64-darwin bin/audius-ctl-x86_64-darwin

.PHONY: ignore-code-gen
ignore-code-gen:
	@echo "Warning: not regenerating .go files from sql, templ, proto, etc. Using existing artifacts instead."
	@touch $(SQL_ARTIFACTS) $(TEMPL_ARTIFACTS) $(PROTO_ARTIFACTS) go.mod

.PHONY: build-wrapper-local build-push-wrapper
build-wrapper-local:
	@echo "Building Docker image for local platform..."
	docker buildx build --load -t audius/audius-d:$(WRAPPER_TAG) pkg/orchestration

build-push-wrapper:
	@echo "Building and pushing Docker images for all platforms..."
	docker buildx build --platform linux/amd64,linux/arm64 --push -t audius/audius-d:$(WRAPPER_TAG) pkg/orchestration

.PHONY: build-audiusd-test build-audiusd-dev
build-audiusd-test:
	DOCKER_DEFAULT_PLATFORM=linux/arm64 docker build --target test --build-arg GIT_SHA=$(AD_TAG) -t audius/audiusd:test-local -f ./cmd/audiusd/Dockerfile ./
build-audiusd-dev:
	DOCKER_DEFAULT_PLATFORM=linux/arm64 docker build --target dev --build-arg GIT_SHA=$(AD_TAG) -t audius/audiusd:dev -f ./cmd/audiusd/Dockerfile ./

.PHONY: build-push-audiusd build-push-cpp
build-push-audiusd:
	docker build \
		--build-arg GIT_SHA=$(AD_TAG) \
		-t audius/audiusd:$(AD_TAG) \
		-f ./cmd/audiusd/Dockerfile ./
	docker push audius/audiusd:$(AD_TAG)

build-push-cpp:
	docker buildx build --platform linux/amd64,linux/arm64 --push -t audius/cpp:bookworm -f ./cmd/audiusd/Dockerfile.deps ./


.PHONY: force-release-stage force-release-foundation force-release-sps
force-release-stage:
	@bash scripts/release-audiusd.sh $@

force-release-foundation:
	@bash scripts/release-audiusd.sh $@

force-release-sps:
	@bash scripts/release-audiusd.sh $@


.PHONY: install uninstall
install:
	@bash scripts/install-audius-ctl.sh local

uninstall:
	@bash scripts/uninstall-audius-ctl.sh

.PHONY: clean
clean:
	rm -f bin/*

.PHONY: install-deps install-go-deps
install-deps: install-go-deps
	@brew install protobuf
	@brew install crane
	@brew install bufbuild/buf/buf
	@gookme init --types pre-commit,pre-push || echo "Gookme init failed, check if it's installed (https://lmaxence.github.io/gookme)"

install-go-deps:
	go install -v github.com/onsi/ginkgo/v2/ginkgo@v2.19.0
	go install -v github.com/sqlc-dev/sqlc/cmd/sqlc@latest
	go install -v google.golang.org/protobuf/cmd/protoc-gen-go@latest
	go install -v google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
	go install -v github.com/cortesi/modd/cmd/modd@latest
	go install -v github.com/a-h/templ/cmd/templ@latest
	go install -v github.com/ethereum/go-ethereum/cmd/abigen@latest
	go install -v github.com/go-swagger/go-swagger/cmd/swagger@latest

go.sum: go.mod
go.mod: $(GO_SRCS)
	@# dummy go.mod file to speed up tidy times
	@[ -d node_modules ] && touch node_modules/go.mod || true
	go mod tidy
	@touch go.mod # in case there's nothing to tidy

.PHONY: gen
gen: regen-templ regen-proto regen-sql regen-go

.PHONY: regen-templ
regen-templ: $(TEMPL_ARTIFACTS)
$(TEMPL_ARTIFACTS): $(TEMPL_SRCS)
	@echo Regenerating templ code
	cd pkg/core/console && go generate ./...

.PHONY: regen-proto
regen-proto: $(PROTO_ARTIFACTS)
$(PROTO_ARTIFACTS): $(PROTO_SRCS)
	@echo Regenerating protobuf code
	cd pkg/core && buf --version && buf generate
	cd pkg/core/gen/core_proto && swagger generate client -f protocol.swagger.json -t ../ --client-package=core_openapi

.PHONY: regen-sql
regen-sql: $(SQL_ARTIFACTS)
$(SQL_ARTIFACTS): $(SQL_SRCS)
	@echo Regenerating sql code
	cd pkg/core/db && sqlc generate

.PHONY: regen-go
regen-go:
	cd pkg/core && go generate ./...

.PHONY: test-down
test-down:
	@docker compose \
    	--file='dev-tools/dev/docker-compose.yml' \
        --project-name='audiusd-test' \
        --project-directory='./' \
		--profile=* \
        down -v

##############
## AUDIUSD  ##
##############

.PHONY: audiusd-dev
audiusd-dev: audiusd-dev-down build-audiusd-dev
	@docker compose \
		--file='dev/docker-compose.yml' \
		--project-name='audiusd' \
		--project-directory='./' \
		--profile=audiusd-dev \
		up -d

audiusd-dev-down:
	@docker compose \
		--file='dev/docker-compose.yml' \
		--project-name='audiusd' \
		--project-directory='./' \
		--profile=audiusd-dev \
		down -v

##############
## MEDIORUM ##
##############

.PHONY: mediorum-test
mediorum-test:
	@docker compose \
		--file='dev/docker-compose.yml' \
		--project-name='audiusd-test' \
		--project-directory='./' \
		--profile=mediorum-unittests \
		run $(TTY_FLAG) --rm test-mediorum-unittests
	@echo 'Tests successful. Spinning down containers...'
	@docker compose \
    	--file='dev/docker-compose.yml' \
        --project-name='audiusd-test' \
        --project-directory='./' \
		--profile=mediorum-unittests \
        down -v

##########
## CORE ##
##########

.PHONY: core-build-native
core-build-native: bin/core
bin/core: $(BUILD_SRCS)
	@go build -ldflags "$(VERSION_LDFLAG)" -o bin/core ./cmd/core/main.go

.PHONY: core-build-amd64
core-build-amd64: bin/core-amd64
bin/core-amd64: $(BUILD_SRCS)
	@GOOS=linux GOARCH=amd64 go build -ldflags "$(VERSION_LDFLAG)" -o bin/core-amd64 ./cmd/core/main.go

.PHONY: core-test
core-test:
	@docker compose \
		--file='dev/docker-compose.yml' \
		--project-name='audiusd-test' \
		--project-directory='./' \
		--profile=core-tests \
		run $(TTY_FLAG) --rm test-core
	@echo 'Tests complete. Spinning down containers...'
	@docker compose \
		--file='dev/docker-compose.yml' \
		--project-name='audiusd-test' \
		--project-directory='./' \
		--profile=core-tests \
		down -v

#############################
## Audio Analysis Backfill ##
#############################

.PHONY: release-aa-backfill
release-aa-backfill:
	@DOCKER_DEFAULT_PLATFORM=linux/amd64 docker build -t audius/audio-analysis-backfill:latest -f ./cmd/audio-analysis-backfill/Dockerfile .
	@docker push audius/audio-analysis-backfill:latest


# Creates the eth-ganache and poa-ganache images to be used for local dev.
# This first creates arm images (assuming this is ran on a mac) and pushes those to a latest-arm image tag on dockerhub.
# Second, the same process is done but for amd64 images. These are pushed to a latest-amd image tag on dockerhub.
# Lastly, both multiarch manifests are created for each image type (poa and eth). This allows a single latest tag on
# dockerhub to have image references for both builds. This allows us to pull an arm image when running local dev on macs.
# It also allows CI to pull the amd builds when it runs on pull requests, merges, and releases.
# This script serves as documentation as to how these manifests and images were created but it likely never needs to be run again.
# Should either the poa or eth ganache containers need updating, simply run this on a mac while logged into the audius dockerhub account.
.PHONY: static-deps
static-deps:
	@echo "Building linux/arm64 images"
	audius-compose build eth-ganache poa-ganache
	docker tag audius/eth-ganache:latest audius/eth-ganache:latest-arm
	docker tag audius/poa-ganache:latest audius/poa-ganache:latest-arm
	docker push audius/eth-ganache:latest-arm
	docker push audius/poa-ganache:latest-arm

	@echo "Building linux/amd64 images"
	DOCKER_DEFAULT_PLATFORM=linux/amd64 audius-compose build eth-ganache poa-ganache
	docker tag audius/eth-ganache:latest audius/eth-ganache:latest-amd
	docker tag audius/poa-ganache:latest audius/poa-ganache:latest-amd
	docker push audius/eth-ganache:latest-amd
	docker push audius/poa-ganache:latest-amd

	@echo "Creating multi-architecture manifest for poa-ganache..."
	docker manifest create audius/poa-ganache:latest \
		audius/poa-ganache:latest-amd \
		audius/poa-ganache:latest-arm
	docker manifest push audius/poa-ganache:latest
	@echo "Pushed audius/poa-ganache:latest"

	@echo "Creating multi-architecture manifest for eth-ganache..."
	docker manifest create audius/eth-ganache:latest \
		audius/eth-ganache:latest-amd \
		audius/eth-ganache:latest-arm
	docker manifest push audius/eth-ganache:latest
	@echo "Pushed audius/eth-ganache:latest"

