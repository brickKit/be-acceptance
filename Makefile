PROJECT ?= ../..
PYTHON ?= python3

.PHONY: models doc-checks help

help:  ## List targets
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "%-12s %s\n", $$1, $$2}'

models:  ## Run every reference model (model 35 takes about 13 minutes)
	@set -e; for m in models/*.py; do echo "== $$m"; $(PYTHON) $$m; done

doc-checks:  ## Run the document checks against PROJECT (default: the assembly project root when checked out at tools/be-acceptance)
	@$(PYTHON) doc-checks/33-expectation-ids.py $(PROJECT)
	@$(PYTHON) doc-checks/34-dependency-closure.py $(PROJECT)
	@$(PYTHON) doc-checks/links.py $(PROJECT)
