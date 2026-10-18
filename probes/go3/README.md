# test/probe-go3

`test/probe-go` 的变体（be-acceptance 仓库），同一份代码、另一个组件 ID。

## 在项目里使用

在装配项目的 `brickkit.yaml` 里加一个写 `repo` 的安装源指向 be-acceptance 仓库，然后 `brickkit add test/probe-go3@<版本>`；版本是标签 `test-probe-go3/<版本>`。

## 文档

| 问题 | 看哪里 |
| --- | --- |
| 它是什么、怎样部署与配置 | `BRICKKIT.md` |
| 主探针与契约 | be-acceptance 仓库的 `probes/go/` 与 `probes/CONTRACT.md` |

## 开发

见 `AGENTS.md`。
