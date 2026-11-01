PROJECT ?= ../..
PYTHON ?= python3

.PHONY: models doc-checks probes scenarios check help

help:  ## List targets
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "%-12s %s\n", $$1, $$2}'

models:  ## Run every reference model (model 35 takes about 13 minutes)
	@set -e; for m in models/*.py; do echo "== $$m"; $(PYTHON) $$m; done

doc-checks:  ## Run the document checks against PROJECT (default: the assembly project root when checked out at tools/be-acceptance)
	@$(PYTHON) doc-checks/33-expectation-ids.py $(PROJECT)
	@$(PYTHON) doc-checks/34-dependency-closure.py $(PROJECT)
	@$(PYTHON) doc-checks/links.py $(PROJECT)

probes:  ## Run both probes alone and together (needs BE_ACC_PG_ADMIN, BE_ACC_NATS_URL, BE_ACC_S3_URL, BE_ACC_S3_ACCESS_KEY, BE_ACC_S3_SECRET_KEY)
	@cd conformance && uv run pytest -q tests

scenarios:  ## L3 business chains against a running, seeded cell (needs BE_ACC_CELL, BE_ACC_IAM_TEST_PASSWORD)
	@cd conformance && uv run pytest -q scenarios

# The tests that need no base resources: the probes, the profile and the backlog count talk to PostgreSQL, NATS and
# object storage (`make probes`). 1.0.18 was tagged without them and broke test_units (dev/rebuild/retro-step12.md, P6).
check:  ## Before a v tag: the tests that need no base resources, and the probe variants up to date
	@cd probes && $(PYTHON) variants.py --check
	@cd conformance && uv run pytest -q tests --ignore=tests/test_probes.py --ignore=tests/test_profile.py \
		--deselect tests/test_measure.py::test_backlog_counts_what_no_one_acknowledged
