# Dev / CI helpers. `src/odioctl/__init__.py` is the version source of truth;
# this Makefile drives lint / test / build / deb and keeps debian/changelog
# in sync with the Python version, no logic duplicated in the workflow YAML.

PYTHON  ?= python3
VERSION := $(PYTHON) scripts/version.py

.PHONY: version deb-version check-tag sync-deb sudoers check-sudoers \
        lint lint-ruff lint-mypy test build deb clean

# --- version helpers ---------------------------------------------------

version:
	@$(VERSION)

deb-version:
	@$(VERSION) --debian

# Fail if the git tag doesn't match __init__.py (vX prefix optional).
TAG ?= $(GITHUB_REF_NAME)
check-tag:
	@$(VERSION) --check-tag '$(TAG)'

# Bump debian/changelog to match deb-version. Idempotent — noop if already
# in sync. Needs `devscripts` (dch) and `dpkg-dev` (dpkg-parsechangelog).
sync-deb:
	@deb=$$($(VERSION) --debian); \
	cl=$$(dpkg-parsechangelog -S Version); \
	if [ "$$deb" != "$$cl" ]; then \
		dch -b --newversion "$$deb" --distribution unstable \
			--urgency medium "Release $$deb"; \
	fi

# --- generated files ---------------------------------------------------

# data/sudoers/odioctl lists one line per DAC id (no wildcards) — regenerate
# after touching dac.CATALOG.
sudoers:
	$(PYTHON) scripts/gen-sudoers.py

check-sudoers:
	$(PYTHON) scripts/gen-sudoers.py --check

# --- dev workflow ------------------------------------------------------

lint: lint-ruff lint-mypy

lint-ruff:
	ruff check src tests scripts
	ruff format --check src tests scripts

lint-mypy:
	mypy

test:
	pytest -q

build:
	$(PYTHON) -m build

# Builds the .deb via dpkg-buildpackage. Requires a Debian toolchain
# (debhelper, dh-python, pybuild-plugin-pyproject, python3-hatchling) —
# use a debian:trixie container on non-Debian hosts. Does NOT call
# `sync-deb`; run it first for a release build.
deb:
	dpkg-buildpackage -b -us -uc

clean:
	rm -rf build/ dist/ *.egg-info .pybuild debian/odioctl debian/.debhelper \
	       debian/files debian/*.substvars debian/*.debhelper.log debian/debhelper-build-stamp
