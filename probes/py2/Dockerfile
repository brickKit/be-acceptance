# The Python probe built at its release tag (the code lives in probes/py); COMPONENT_ID picks this variant.
FROM python:3.14.4-alpine AS build
COPY --from=ghcr.io/astral-sh/uv:0.12.1 /uv /bin/uv
RUN apk add --no-cache git && git clone -q --depth 1 --branch test-probe-py/3.2.2 https://github.com/brickKit/be-acceptance /src \
    && mkdir -p /app && cp -r /src/probes/py/. /app
WORKDIR /app
RUN uv sync --frozen --no-dev --no-editable

FROM python:3.14.4-alpine
COPY --from=build /app/.venv /app/.venv
ENV PATH=/app/.venv/bin:$PATH PORT=8605
EXPOSE 8605 9605
ENTRYPOINT ["python", "-m", "probe_py"]
