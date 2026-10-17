PROJECT ?= ../..
PYTHON ?= python3

.PHONY: models doc-checks probes help

help:  ## List targets
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "%-12s %s\n", $$1, $$2}'

models:  ## Run every reference model (model 35 takes about 13 minutes)
	@set -e; for m in models/*.py; do echo "== $$m"; $(PYTHON) $$m; done

doc-checks:  ## Run the document checks against PROJECT (default: the assembly project root when checked out at tools/be-acceptance)
	@$(PYTHON) doc-checks/33-expectation-ids.py $(PROJECT)
	@$(PYTHON) doc-checks/34-dependency-closure.py $(PROJECT)
	@$(PYTHON) doc-checks/links.py $(PROJECT)

probes:  ## Run both probes alone and together (needs BE_ACC_PG_ADMIN, BE_ACC_NATS_URL, BE_ACC_S3_URL, BE_ACC_S3_ACCESS_KEY, BE_ACC_S3_SECRET_KEY)
	@cd conformance && uv run pytest -q
