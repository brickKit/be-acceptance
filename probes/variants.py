"""Writes the probe variants' component directories (go2, go3, go4, py2) from the main probes: the same manifest with
the variant's ID, name, ports and migration, a Dockerfile that builds the main probe at its release tag, and short
docs. Run it after changing a main probe's manifest or version: `python3 variants.py`; the conformance tests check
that the directories are up to date (`python3 variants.py --check`). Standard library only."""
from __future__ import annotations

import pathlib
import re
import sys

HERE = pathlib.Path(__file__).resolve().parent
# id suffix -> (language, http port, grpc port); the main probes are go (8600/9600) and py (8601/9601)
VARIANTS = {"go2": ("go", 8602, 9602), "go3": ("go", 8603, 9603), "go4": ("go", 8604, 9604), "py2": ("py", 8605, 9605)}
LANG = {"go": "Go", "py": "Python"}

DOCKER = {
    "go": """# The Go probe built at its release tag (the code lives in probes/go); COMPONENT_ID picks this variant.
FROM golang:1.27.2-alpine AS build
RUN CGO_ENABLED=0 GOBIN=/out go install -trimpath github.com/brickKit/be-acceptance/probes/go/v3@v{version}

FROM alpine:3.24.2
COPY --from=build /out/go /probe-go
ENV PORT={port}
EXPOSE {port} {grpc}
ENTRYPOINT ["/probe-go"]
""",
    "py": """# The Python probe built at its release tag (the code lives in probes/py); COMPONENT_ID picks this variant. The same
# slim base as probes/py; wget for the platform's health check; no PORT (BrickKit sets it, CLI 1.10.1+).
FROM python:3.15.0-slim-trixie AS build
COPY --from=ghcr.io/astral-sh/uv:0.13.1 /uv /bin/uv
RUN apt-get update && apt-get install -y --no-install-recommends git ca-certificates && rm -rf /var/lib/apt/lists/* \\
    && git clone -q --depth 1 --branch test-probe-py/{version} https://github.com/brickKit/be-acceptance /src \\
    && mkdir -p /app && cp -r /src/probes/py/. /app
WORKDIR /app
RUN uv sync --frozen --no-dev --no-editable

FROM python:3.15.0-slim-trixie
RUN apt-get update && apt-get install -y --no-install-recommends wget && rm -rf /var/lib/apt/lists/*
COPY --from=build /app/.venv /app/.venv
ENV PATH=/app/.venv/bin:$PATH
EXPOSE {port} {grpc}
ENTRYPOINT ["python", "-m", "probe_py"]
""",
}

# go refuses `go run ../go` outside a module (a variant's directory holds only its manifest): run inside ../go
LOCAL = {"go": ("[go, -C, ../go, run, .]", "[go, -C, ../go, run, ., migrate]"),
         "py": ("[uv, run, --project, ../py, python, -m, probe_py]",
                "[uv, run, --project, ../py, python, -m, probe_py, migrate]")}


def manifest(kind: str, lang: str, port: int, grpc: int) -> str:
    base = (HERE / lang / "component.yaml").read_text()
    pid = f"test/probe-{kind}"
    out = base.replace(f"id: test/probe-{lang}\n", f"id: {pid}\n", 1)
    out = re.sub(r"(?m)^  name: .*$", f"  name: {LANG[lang]} probe ({kind})", out, count=1)
    out = re.sub(r'(?m)^  description: .*$', f'  description: "A variant of test/probe-{lang}: the same code under its own '
                 f'component ID, ports and event stream"', out, count=1)
    out = re.sub(r"(?m)^  port: \d+$", f"  port: {port}", out, count=1)
    out = re.sub(r"(?m)^      port: \d+$", f"      port: {grpc}", out, count=1)
    run, mig = LOCAL[lang]
    out = re.sub(r"(?m)^  runCommand: .*$", f"  runCommand: {run}", out, count=1)
    out = re.sub(r"(?m)^  migrationCommand: .*$", f"  migrationCommand: {mig}", out, count=1)
    return out


def version(lang: str) -> str:
    return re.search(r"(?m)^  version: (\S+)$", (HERE / lang / "component.yaml").read_text()).group(1)


def docs(kind: str, lang: str) -> dict[str, str]:
    pid = f"test/probe-{kind}"
    return {
        "BRICKKIT.md": f"""# {pid}

## 组件定位

`test/probe-{lang}` 的变体：同一份代码（be-acceptance 仓库的 `probes/{lang}/`）以另一个组件 ID、端口、schema 与事件流 `test.probe_{kind}` 运行，用来让一个外壳承载多个探针、让两个探针在同一进程里互相调用。行为、契约与配置都与 `test/probe-{lang}` 相同（be-acceptance 仓库的 `probes/CONTRACT.md`）。

## 部署前准备

同 `test/probe-{lang}`：自己的 schema 与角色（注册表的 `test_probe_{kind}`）、消息系统、对象存储的桶与前缀、部署的服务证书。

## 依赖说明

不声明组件依赖：下一个探针由部署用配置指定（`PROBE_NEXT`、`PROBE_NEXT_URL`、`PROBE_NEXT_GRPC_URL`，协议 P4.26）。

## 配置指南

键与 `test/probe-{lang}` 相同（`component.yaml` 的 `configSchema`）。必填：`PG_HOST`、`PG_DATABASE`、`PG_USER`、`PG_PASSWORD`、`PG_SCHEMA`（自己的数据库目标、运行角色与 schema，密码是密钥）；`BUS_URL`（消息系统，密钥）；`IMPLEMENTATION_INSTANCE`（生产者实例的身份）。

## 契约索引

同 `test/probe-{lang}`；资源类型与操作键的前缀是 `test.probe_{kind}`，事件流是 `test.probe_{kind}`。

## 外壳声明

不是外壳。可以被{LANG[lang]}外壳承载。
""",
        "AGENTS.md": f"""# {pid}

`test/probe-{lang}` 的变体，没有自己的代码；这个目录由 be-acceptance 仓库的 `probes/variants.py` 生成，改主探针后重新生成，不手改。

## 代码地图

| 路径 | 内容 |
| --- | --- |
| `component.yaml` | 清单：由主探针的清单换成本变体的 ID、端口与迁移命令 |
| `Dockerfile` | 按发布标签构建主探针；入口按 `COMPONENT_ID` 选本变体 |

## 构建与测试

代码与测试都在主探针（`probes/{lang}/`）与 `conformance/`；镜像要在主探针的发布标签存在之后构建。

## 设计取舍

- 变体不复制代码：同一份代码保证测出的差异只来自拓扑。
- 版本与主探针相同，同一次发布打标签。

## 易错点

| 绝不 | 症状 | 为什么 |
| --- | --- | --- |
| 手改这个目录 | 下次生成时被覆盖，或与主探针不一致而测试失败 | 由 `probes/variants.py` 生成 |

## 改代码前自查

- 改的其实是主探针吗？
- 改完重新生成并跑 `conformance/` 的测试了吗？

<!-- brickkit:managed:begin lang=zh -->
<!-- brickkit:managed:end -->
""",
        "CLAUDE.md": "@AGENTS.md\n",
        "README.md": f"""# {pid}

`test/probe-{lang}` 的变体（be-acceptance 仓库），同一份代码、另一个组件 ID。

## 在项目里使用

在装配项目的 `brickkit.yaml` 里加一个写 `repo` 的安装源指向 be-acceptance 仓库，然后 `brickkit add {pid}@<版本>`；版本是标签 `test-probe-{kind}/<版本>`。

## 文档

| 问题 | 看哪里 |
| --- | --- |
| 它是什么、怎样部署与配置 | `BRICKKIT.md` |
| 主探针与契约 | be-acceptance 仓库的 `probes/{lang}/` 与 `probes/CONTRACT.md` |

## 开发

见 `AGENTS.md`。
""",
    }


def files() -> dict[pathlib.Path, str]:
    out = {}
    for kind, (lang, port, grpc) in VARIANTS.items():
        d = HERE / kind
        out[d / "component.yaml"] = manifest(kind, lang, port, grpc)
        out[d / "Dockerfile"] = DOCKER[lang].format(version=version(lang), port=port, grpc=grpc)
        for name, text in docs(kind, lang).items():
            out[d / name] = text
    return out


def managed(old: str, new: str) -> str:
    """Keeps the brickkit-maintained block of AGENTS.md as brickkit last wrote it."""
    m = re.search(r"<!-- brickkit:managed:begin.*?<!-- brickkit:managed:end -->", old, re.S)
    return re.sub(r"<!-- brickkit:managed:begin.*?<!-- brickkit:managed:end -->", lambda _: m.group(0), new, flags=re.S) if m else new


if __name__ == "__main__":
    stale = []
    for path, text in files().items():
        current = path.read_text() if path.exists() else ""
        if path.name == "AGENTS.md":
            text = managed(current, text)
        if current != text:
            stale.append(path)
            if "--check" not in sys.argv:
                path.parent.mkdir(exist_ok=True)
                path.write_text(text)
    if "--check" in sys.argv and stale:
        sys.exit("out of date: " + ", ".join(str(p.relative_to(HERE)) for p in stale) + " (run python3 variants.py)")
