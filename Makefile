VERSION  := $(shell cat version.txt)
BUILDTAG := \
  $(shell [ -d .git ] && git rev-parse --short=10 HEAD 2>/dev/null || date '+src@%Y%m%d%H%M%S')

BIN_MEMEX_SOURCES := \
  cmd/memex/*.go \
  service-api/*.go \
  extension/*.go \
  extension/*/*.go

SERVICES_TWITTER_SOURCES := \
  services/twitter/*.go \
  services/twitter/tweeter/*.go

ALL_SOURCES := $(BIN_MEMEX_SOURCES) $(SERVICES_TWITTER_SOURCES)

TEST_SRC_FILES := \
  $(shell find . -type f -name '*_test.go' -not -path '*/_*' -not -path './vendor/*')
TEST_DIRS := $(sort $(dir $(TEST_SRC_FILES)))

# ------------------------------------------------------------------------------------------
# products

all: bin/memex services/twitter/twitter

bin/memex: $(BIN_MEMEX_SOURCES)
	@echo "go build $@"
	@go build \
	  -ldflags="-X 'main.MEMEX_VERSION=$(VERSION)' -X 'main.MEMEX_BUILDTAG=$(BUILDTAG)'" \
	   -o "$@" ./cmd/memex/

services/twitter/twitter: $(SERVICES_TWITTER_SOURCES)
	@echo "go build $@"
	@go build -o "$@" ./services/twitter/

clean:
	rm -f bin/*

.PHONY: all clean

# ------------------------------------------------------------------------------------------
# development & maintenance

fmt:
	@gofmt -l -w -s $(ALL_SOURCES)

tidy: fmt
	go mod tidy

test:
	@go test $(TEST_DIRS)


# files that affect building but not the source. Used by dev-* targets
WATCH_MISC_FILES := $(firstword $(MAKEFILE_LIST)) go.sum

run-service: fmt bin/memex
	./bin/memex -debug -D example-memexdir

dev-service:
	@autorun -no-clear $(WATCH_MISC_FILES) $(DEV_CONFIG) $(SERVICE_SOURCES) -- \
	"$(MAKE) run-service"

.PHONY: fmt tidy test
.PHONY: run-service dev-service
