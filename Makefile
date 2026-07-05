SHELL := /bin/bash

GO        ?= go
PKG       := ./...
COVER_OUT ?= coverage.out
COVER_MIN ?= 30

.PHONY: help lint vet test cover check-adrs all ci-local clean

help:
	@echo "Targets:"
	@echo "  lint        - go vet (golangci-lint when installed)"
	@echo "  vet         - go vet"
	@echo "  test        - go test with the race detector"
	@echo "  cover       - go test with coverage, enforce \$$COVER_MIN ($(COVER_MIN)%)"
	@echo "  check-adrs  - verify the ADR README index matches the folder"
	@echo "  ci-local    - the same checks CI runs, in the same order"

HAS_GO := $(shell find . -name '*.go' -not -path './.git/*' -print -quit)

lint:
ifneq ($(HAS_GO),)
	@if command -v golangci-lint >/dev/null 2>&1; then \
	  golangci-lint run; \
	else \
	  $(GO) vet $(PKG); \
	fi
else
	@echo "no go files to lint -- skipping"
endif

vet:
ifneq ($(HAS_GO),)
	$(GO) vet $(PKG)
endif

test:
ifneq ($(HAS_GO),)
	$(GO) test -race -count=1 $(PKG)
endif

cover:
ifneq ($(HAS_GO),)
	$(GO) test -race -count=1 -covermode=atomic -coverprofile=$(COVER_OUT) $(PKG)
	@grep -v '/cmd/' $(COVER_OUT) > $(COVER_OUT).prod || true
	@mv $(COVER_OUT).prod $(COVER_OUT)
	@if [ "$$(wc -l < $(COVER_OUT) | tr -d ' ')" -le 1 ]; then \
	  echo "coverage: no executable statements -- vacuously pass"; \
	  exit 0; \
	fi; \
	total=$$($(GO) tool cover -func=$(COVER_OUT) | awk '/^total:/{print $$3}' | tr -d '%'); \
	if [ -z "$$total" ]; then \
	  echo "coverage: total unmeasurable -- vacuously pass"; \
	  exit 0; \
	fi; \
	echo "coverage: $$total%"; \
	awk -v have="$$total" -v need="$(COVER_MIN)" 'BEGIN{ exit (have+0 >= need+0) ? 0 : 1 }' \
	  || { echo "coverage below threshold ($(COVER_MIN)%)"; exit 1; }
endif

check-adrs:
	@bash scripts/check-adrs.sh

all: lint vet test cover check-adrs

ci-local: all

clean:
	rm -f $(COVER_OUT)
