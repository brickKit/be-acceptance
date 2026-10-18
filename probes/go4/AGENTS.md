# test/probe-go4

`test/probe-go` 的变体，没有自己的代码；这个目录由 be-acceptance 仓库的 `probes/variants.py` 生成，改主探针后重新生成，不手改。

## 代码地图

| 路径 | 内容 |
| --- | --- |
| `component.yaml` | 清单：由主探针的清单换成本变体的 ID、端口与迁移命令 |
| `Dockerfile` | 按发布标签构建主探针；入口按 `COMPONENT_ID` 选本变体 |

## 构建与测试

代码与测试都在主探针（`probes/go/`）与 `conformance/`；镜像要在主探针的发布标签存在之后构建。

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
<!-- 由 brickkit 维护（init、add、remove、upgrade、skills update）：这对标记之间的改动会被覆盖 -->

## BrickKit

这是一个 BrickKit 组件：平台只读 `component.yaml`。它依赖的规则：

- `configSchema` 的键就是代码读的环境变量名。不能用保留名：`COMPONENT_ID`、`COMPONENT_VERSION`、`PORT`、`BRICKKIT_SERVED_MEMBERS`、`BRICKKIT_SERVED_MEMBERS_CONFIG`，以及任何 `*_ENDPOINT`。
- 依赖写精确版本。依赖的地址以 `<ID>_ENDPOINT` 注入；缺席的可选依赖根本没有这个变量，读的时候要带兜底。
- `/healthz` 只查本进程，不查依赖。迁移命令用同一个镜像跑，遇到不认识的参数必须直接失败；`local.migrationCommand` 从源码跑同一条迁移，给 `mode: local` 用。
- `BRICKKIT.md` 会随版本进入每个使用它的项目，在那里是脱离仓库单独读的：跟代码一起改，不放相对链接。
- 发版：改 `metadata.version`，提交、推送，`brickkit release`。`brickkit lint` 会检查清单和这些文档——在项目里、在这个目录下跑，只查这个组件（`--all` 查整个项目）。
- 完整规则在 `brickkit-component` 技能里（装了技能的项目或仓库根目录下的 `.claude/skills/brickkit-component/SKILL.md`；`brickkit skills update` 会装上）；参数问 `brickkit <命令> --help`；BrickKit 自己的文档用 `brickkit docs` 看。
<!-- brickkit:managed:end -->
