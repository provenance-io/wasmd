#!/usr/bin/make -f

PACKAGES_SIMTEST=$(shell go list ./... | grep '/simulation')
VERSION := $(shell echo $(shell git describe --tags) | sed 's/^v//')
COMMIT := $(shell git log -1 --format='%H')
LEDGER_ENABLED ?= true
SDK_PACK := $(shell go list -m github.com/cosmos/cosmos-sdk | sed  's/ /\@/g')

# for dockerized protobuf tools
PROTO_CONTAINER := cosmwasm/prototools-docker:v0.1.0
DOCKER_BUF := docker run --rm -v $(shell pwd)/buf.yaml:/workspace/buf.yaml -v $(shell go list -f "{{ .Dir }}" -m github.com/cosmos/cosmos-sdk):/workspace/cosmos_sdk_dir -v $(shell pwd):/workspace/wasmd  --workdir /workspace $(PROTO_CONTAINER)
HTTPS_GIT := https://github.com/CosmWasm/wasmd.git

export GO111MODULE = on

# process build tags

build_tags = netgo
ifeq ($(LEDGER_ENABLED),true)
  ifeq ($(OS),Windows_NT)
    GCCEXE = $(shell where gcc.exe 2> NUL)
    ifeq ($(GCCEXE),)
      $(error gcc.exe not installed for ledger support, please install or set LEDGER_ENABLED=false)
    else
      build_tags += ledger
    endif
  else
    UNAME_S = $(shell uname -s)
    ifeq ($(UNAME_S),OpenBSD)
      $(warning OpenBSD detected, disabling ledger support (https://github.com/cosmos/cosmos-sdk/issues/1988))
    else
      GCC = $(shell command -v gcc 2> /dev/null)
      ifeq ($(GCC),)
        $(error gcc not installed for ledger support, please install or set LEDGER_ENABLED=false)
      else
        build_tags += ledger
      endif
    endif
  endif
endif

ifeq ($(WITH_CLEVELDB),yes)
  build_tags += gcc
endif
build_tags += $(BUILD_TAGS)
build_tags := $(strip $(build_tags))

whitespace :=
empty = $(whitespace) $(whitespace)
comma := ,
build_tags_comma_sep := $(subst $(empty),$(comma),$(build_tags))

# process linker flags

ldflags = -X github.com/cosmos/cosmos-sdk/version.Name=wasm \
		  -X github.com/cosmos/cosmos-sdk/version.AppName=wasmd \
		  -X github.com/cosmos/cosmos-sdk/version.Version=$(VERSION) \
		  -X github.com/cosmos/cosmos-sdk/version.Commit=$(COMMIT) \
		  -X github.com/CosmWasm/wasmd/app.Bech32Prefix=cosmos \
		  -X "github.com/cosmos/cosmos-sdk/version.BuildTags=$(build_tags_comma_sep)"

ifeq ($(WITH_CLEVELDB),yes)
  ldflags += -X github.com/cosmos/cosmos-sdk/types.DBBackend=cleveldb
endif
ldflags += $(LDFLAGS)
ldflags := $(strip $(ldflags))

coral_ldflags = -X github.com/cosmos/cosmos-sdk/version.Name=coral \
				  -X github.com/cosmos/cosmos-sdk/version.AppName=corald \
				  -X github.com/cosmos/cosmos-sdk/version.Version=$(VERSION) \
				  -X github.com/cosmos/cosmos-sdk/version.Commit=$(COMMIT) \
				  -X github.com/CosmWasm/wasmd/app.NodeDir=.corald \
				  -X github.com/CosmWasm/wasmd/app.Bech32Prefix=coral \
				  -X "github.com/cosmos/cosmos-sdk/version.BuildTags=$(build_tags_comma_sep)"
# we could consider enabling governance override?
#				  -X github.com/CosmWasm/wasmd/app.EnableSpecificProposals=MigrateContract,UpdateAdmin,ClearAdmin \

coral_ldflags += $(LDFLAGS)
coral_ldflags := $(strip $(coral_ldflags))

flex_ldflags = -X github.com/cosmos/cosmos-sdk/version.Name=gaiaflex \
				  -X github.com/cosmos/cosmos-sdk/version.AppName=gaiaflexd \
				  -X github.com/cosmos/cosmos-sdk/version.Version=$(VERSION) \
				  -X github.com/cosmos/cosmos-sdk/version.Commit=$(COMMIT) \
				  -X github.com/CosmWasm/wasmd/app.ProposalsEnabled=true \
				  -X github.com/CosmWasm/wasmd/app.NodeDir=.gaiaflexd \
				  -X github.com/CosmWasm/wasmd/app.Bech32Prefix=cosmos \
				  -X "github.com/cosmos/cosmos-sdk/version.BuildTags=$(build_tags_comma_sep)"

flex_ldflags += $(LDFLAGS)
flex_ldflags := $(strip $(flex_ldflags))

BUILD_FLAGS := -tags "$(build_tags_comma_sep)" -ldflags '$(ldflags)' -trimpath
CORAL_BUILD_FLAGS := -tags "$(build_tags_comma_sep)" -ldflags '$(coral_ldflags)' -trimpath
FLEX_BUILD_FLAGS := -tags "$(build_tags_comma_sep)" -ldflags '$(flex_ldflags)' -trimpath

all: install lint test

build: go.sum
ifeq ($(OS),Windows_NT)
	exit 1
else
	go build -mod=readonly $(BUILD_FLAGS) -o build/wasmd ./cmd/wasmd
endif

build-coral: go.sum
ifeq ($(OS),Windows_NT)
	exit 1
else
	go build -mod=readonly $(CORAL_BUILD_FLAGS) -o build/corald ./cmd/wasmd
endif

build-gaiaflex: go.sum
ifeq ($(OS),Windows_NT)
	exit 1
else
	go build -mod=readonly $(FLEX_BUILD_FLAGS) -o build/gaiaflexd ./cmd/wasmd
endif

build-linux: go.sum
	LEDGER_ENABLED=false GOOS=linux GOARCH=amd64 $(MAKE) build

build-contract-tests-hooks:
ifeq ($(OS),Windows_NT)
	go build -mod=readonly $(BUILD_FLAGS) -o build/contract_tests.exe ./cmd/contract_tests
else
	go build -mod=readonly $(BUILD_FLAGS) -o build/contract_tests ./cmd/contract_tests
endif

install: go.sum
	go install -mod=readonly $(BUILD_FLAGS) ./cmd/wasmd

########################################
### Tools & dependencies

go-mod-cache: go.sum
	@echo "--> Download go modules to local cache"
	@go mod download

go.sum: go.mod
	@echo "--> Ensure dependencies have not been modified"
	@go mod verify

draw-deps:
	@# requires brew install graphviz or apt-get install graphviz
	go get github.com/RobotsAndPencils/goviz
	@goviz -i ./cmd/wasmd -d 2 | dot -Tpng -o dependency-graph.png

clean:
	rm -rf snapcraft-local.yaml build/

distclean: clean
	rm -rf vendor/

########################################
### Testing


test: test-unit
test-all: check test-race test-cover

test-unit:
	@VERSION=$(VERSION) go test -mod=readonly -tags='ledger test_ledger_mock' ./...

test-race:
	@VERSION=$(VERSION) go test -mod=readonly -race -tags='ledger test_ledger_mock' ./...

test-cover:
	@go test -mod=readonly -timeout 30m -race -coverprofile=coverage.txt -covermode=atomic -tags='ledger test_ledger_mock' ./...


benchmark:
	@go test -mod=readonly -bench=. ./...


###############################################################################
###                                Linting                                  ###
###############################################################################

lint:
	golangci-lint run
	find . -name '*.go' -type f -not -path "./vendor*" -not -path "*.git*" | xargs gofmt -d -s

format:
	find . -name '*.go' -type f -not -path "./vendor*" -not -path "*.git*" -not -path "./client/lcd/statik/statik.go" | xargs gofmt -w -s
	find . -name '*.go' -type f -not -path "./vendor*" -not -path "*.git*" -not -path "./client/lcd/statik/statik.go" | xargs misspell -w
	find . -name '*.go' -type f -not -path "./vendor*" -not -path "*.git*" -not -path "./client/lcd/statik/statik.go" | xargs goimports -w -local github.com/CosmWasm/wasmd


###############################################################################
###                                Protobuf                                 ###
###############################################################################

proto-all: proto-gen proto-lint proto-check-breaking
.PHONY: proto-all

proto-gen: proto-lint
	@docker run --rm -v $(shell go list -f "{{ .Dir }}" -m github.com/cosmos/cosmos-sdk):/workspace/cosmos_sdk_dir -v $(shell pwd):/workspace --workdir /workspace --env COSMOS_SDK_DIR=/workspace/cosmos_sdk_dir $(PROTO_CONTAINER) ./scripts/protocgen.sh
.PHONY: proto-gen

proto-lint:
	@$(DOCKER_BUF) buf check lint --error-format=json
.PHONY: proto-lint

proto-check-breaking:
	@$(DOCKER_BUF) buf check breaking --against-input $(HTTPS_GIT)#branch=master
.PHONY: proto-check-breaking

.PHONY: all build-linux install install-debug \
	go-mod-cache draw-deps clean build format \
	test test-all test-build test-cover test-unit test-race \
	test-sim-custom-genesis-fast

#######################
SIMAPP = ./app
BINDIR ?= $(GOPATH)/bin
RUNSIM         = $(TOOLS_DESTDIR)/runsim
# Install the runsim binary with a temporary workaround of entering an outside
# directory as the "go get" command ignores the -mod option and will polute the
# go.{mod, sum} files.
#
# ref: https://github.com/golang/go/issues/30515
runsim: $(RUNSIM)
$(RUNSIM):
	@echo "Installing runsim..."
	@(cd /tmp && go get github.com/cosmos/tools/cmd/runsim@v1.0.0)



test-sim-nondeterminism:
	@echo "Running non-determinism test..."
	@go test -mod=readonly $(SIMAPP) -run TestAppStateDeterminism -Enabled=true \
		-NumBlocks=100 -BlockSize=200 -Commit=true -Period=0 -v -timeout 24h

test-sim-custom-genesis-fast:
	@echo "Running custom genesis simulation..."
	@echo "By default, ${HOME}/.wasmd/config/genesis.json will be used."
	@go test -mod=readonly $(SIMAPP) -run TestFullAppSimulation -Genesis=${HOME}/.wasmd/config/genesis.json \
		-Enabled=true -NumBlocks=100 -BlockSize=200 -Commit=true -Seed=99 -Period=5 -v -timeout 24h

test-sim-import-export: runsim
	@echo "Running application import/export simulation. This may take several minutes..."
	@$(BINDIR)/runsim -Jobs=4 -SimAppPkg=$(SIMAPP) -ExitOnFail 50 5 TestAppImportExport

test-sim-after-import: runsim
	@echo "Running application simulation-after-import. This may take several minutes..."
	@$(BINDIR)/runsim -Jobs=4 -SimAppPkg=$(SIMAPP) -ExitOnFail 50 5 TestAppSimulationAfterImport

test-sim-custom-genesis-multi-seed: runsim
	@echo "Running multi-seed custom genesis simulation..."
	@echo "By default, ${HOME}/.gaiad/config/genesis.json will be used."
	@$(BINDIR)/runsim -Genesis=${HOME}/.gaiad/config/genesis.json -SimAppPkg=$(SIMAPP) -ExitOnFail 400 5 TestFullAppSimulation

test-sim-multi-seed-long: runsim
	@echo "Running long multi-seed application simulation. This may take awhile!"
	@$(BINDIR)/runsim -Jobs=4 -SimAppPkg=$(SIMAPP) -ExitOnFail 500 50 TestFullAppSimulation

test-sim-multi-seed-short: runsim
	@echo "Running short multi-seed application simulation. This may take awhile!"
	@$(BINDIR)/runsim -Jobs=4 -SimAppPkg=$(SIMAPP) -ExitOnFail 50 10 TestFullAppSimulation

test-sim-benchmark-invariants:
	@echo "Running simulation invariant benchmarks..."
	@go test -mod=readonly $(SIMAPP) -benchmem -bench=BenchmarkInvariants -run=^$ \
	-Enabled=true -NumBlocks=1000 -BlockSize=200 \
	-Period=1 -Commit=true -Seed=57 -v -timeout 24h

