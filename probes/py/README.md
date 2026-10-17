# test/probe-py

验收用的 Python 通信探针（be-acceptance 仓库）。

## 在项目里使用

在装配项目的 `brickkit.yaml` 里加一个写 `repo` 的安装源指向 be-acceptance 仓库，然后 `brickkit add test/probe-py@<版本>`；版本是标签 `test-probe-py/<版本>`。

## 文档

| 问题 | 看哪里 |
| --- | --- |
| 它做什么、怎样部署与配置 | `BRICKKIT.md` |
| 怎样改它 | `AGENTS.md` |
| 契约 | be-acceptance 仓库的 `probes/CONTRACT.md` |

## 开发

见 `AGENTS.md` 的“构建与测试”。
