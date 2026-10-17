# test/probe-go

验收用的通信探针。契约在 be-acceptance 仓库的 `probes/CONTRACT.md`，两种语言的探针一起改。

## 代码地图

| 路径 | 内容 |
| --- | --- |
| `main.go` | 单跑入口（`serve`、`migrate`） |
| `probe/defs.go` | 契约常量、配置键、流、资源与操作 |
| `probe/probe.go` | 模块：用户面、系统面、写入与事件、转发工作项 |
| `probe/rpc.go` | 手写的 gRPC 服务描述（不需要生成代码） |
| `probe/migrations/` | 迁移 |
| `Dockerfile` | 镜像（Alpine，健康检查经 `/bin/sh` 与 `wget`） |

## 构建与测试

`go vet ./... && go build .`；单跑：`go run . migrate` 后 `go run .`（需要 `PG_*`、`BUS_URL` 等，见 `component.yaml`）。两个探针一起的测试在 be-acceptance 仓库的 `conformance/`：`uv run pytest`。

## 设计取舍

- 没有业务逻辑：测出的差异只来自语言、拓扑与引擎。
- 用户面缺省公开（`PROBE_GUARD` 关）：探针没有受保护的数据，身份与授权组件就绪之前也能测通信；开启后测带守卫的路径。
- 转发用 SDK 的后台工作项（`probe.relay`，队列类，幂等的外部效果）；网络调用从不在事务内。
- 没有组件依赖：下一个探针由部署指定，实验可以任意组合。

## 易错点

| 绝不 | 症状 | 为什么 |
| --- | --- | --- |
| 只改一种语言的探针 | 两种探针测的不是同一件事，报告的对比失真 | 契约是共同的 |
| 在消费事件的事务里调用下一个探针 | SDK 以 `NETWORK_IN_TX` 拒绝 | 事务内不发网络调用 |
| 模块路径不带主版本（`…/probes/go` 而不是 `…/probes/go/v3`） | 外壳的 Go 构建取不到成员：“module path must match major version”；3.0.0 的模块标签因此不可用，3.0.1 改正 | Go 从主版本 2 起要求路径中的主版本 |
| 手改 `configSchema` | 与 SDK 实际读取的键不一致，测试失败 | 它由 SDK 的键清单生成 |

## 改代码前自查

- 另一种语言的探针是否同样改了？
- 契约文件是否同步？
- `conformance/` 的测试是否两边都过？
- 改了版本号吗（每次改动都升版本）？

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
