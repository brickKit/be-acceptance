# The Python probe built at its release tag (the code lives in probes/py); COMPONENT_ID picks this variant. The same
# slim base as probes/py; wget for the platform's health check; no PORT (BrickKit sets it, CLI 1.10.1+).
FROM python:3.15.0-slim-trixie AS build
COPY --from=ghcr.io/astral-sh/uv:0.13.1 /uv /bin/uv
RUN apt-get update && apt-get install -y --no-install-recommends git ca-certificates && rm -rf /var/lib/apt/lists/* \
    && git clone -q --depth 1 --branch test-probe-py/3.2.20 https://github.com/brickKit/be-acceptance /src \
    && mkdir -p /app && cp -r /src/probes/py/. /app
WORKDIR /app
RUN uv sync --frozen --no-dev --no-editable

FROM python:3.15.0-slim-trixie
RUN apt-get update && apt-get install -y --no-install-recommends wget && rm -rf /var/lib/apt/lists/*
COPY --from=build /app/.venv /app/.venv
ENV PATH=/app/.venv/bin:$PATH
EXPOSE 8605 9605
ENTRYPOINT ["python", "-m", "probe_py"]
