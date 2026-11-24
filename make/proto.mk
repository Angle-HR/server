# Protobuf / gRPC contracts. Include from the main Makefile with: include make/proto.mk
.PHONY: proto proto-lint proto-breaking proto-format

BUF ?= buf
# Baseline for breaking-change checks: the staging branch of this repository.
PROTO_BREAKING_AGAINST ?= .git\#branch=staging,subdir=proto

proto-lint:
	$(BUF) lint

proto-format:
	$(BUF) format -w

# Regenerates gen/ (committed). Plugins run through `go tool`, so no network is needed.
proto: proto-lint
	$(BUF) generate
	@echo "generated code in gen/"

# Fails on wire-incompatible changes versus the baseline. Until staging has a proto/
# directory buf cannot build the baseline; the script treats that as a pass.
proto-breaking:
	@scripts/proto-breaking.sh '$(PROTO_BREAKING_AGAINST)'
