# The Python probe alone: the locked environment built with uv, on the official slim image (glibc, PGO and LTO), the base
# of the project's Python components, on the same Python as the host (3.15.0). wget is for the platform's health check
# (it runs through /bin/sh and wget). No PORT: BrickKit sets it to the manifest's port (CLI 1.10.1+).
FROM python:3.15.0-slim-trixie AS build
COPY --from=ghcr.io/astral-sh/uv:0.13.1 /uv /bin/uv
RUN apt-get update && apt-get install -y --no-install-recommends git ca-certificates && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY pyproject.toml uv.lock ./
RUN uv sync --frozen --no-dev --no-install-project
COPY . .
RUN uv sync --frozen --no-dev --no-editable

FROM python:3.15.0-slim-trixie
RUN apt-get update && apt-get install -y --no-install-recommends wget && rm -rf /var/lib/apt/lists/*
COPY --from=build /app/.venv /app/.venv
ENV PATH=/app/.venv/bin:$PATH
EXPOSE 8601 9601
ENTRYPOINT ["python", "-m", "probe_py"]
