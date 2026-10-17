# The Python probe alone: the locked environment built with uv, run on the same Python as the host (3.14.4).
FROM python:3.14.4-alpine AS build
COPY --from=ghcr.io/astral-sh/uv:0.12.1 /uv /bin/uv
RUN apk add --no-cache git
WORKDIR /app
COPY pyproject.toml uv.lock ./
RUN uv sync --frozen --no-dev --no-install-project
COPY . .
RUN uv sync --frozen --no-dev --no-editable

FROM python:3.14.4-alpine
COPY --from=build /app/.venv /app/.venv
ENV PATH=/app/.venv/bin:$PATH PORT=8601
EXPOSE 8601 9601
ENTRYPOINT ["python", "-m", "probe_py"]
