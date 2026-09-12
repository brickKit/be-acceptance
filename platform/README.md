# platform —— 档 1 平台验收清单（设计书 §9.6.2）

这批用例的被测对象**是 brickKit 本身，不是我们的业务**。每一条对应设计书某处断言，跑一次就知道那处断言还成不成立——**平台升级后重跑，就是回归测试**。

⚠️ **这不是占位符，是任务清单。** 发现平台真有问题时，先在这里记一条用例（哪怕是红的），再回 brickKit 仓库修——不许在业务侧绕过去。绕过去的那一条，会在 62 个组件时变成 61 处绕法（设计书 §9.6.2）。

## 现状（阶段二 Task 19-20 的 20 条 + 阶段三 Task 15 补的 3 条，共 23 条全部落地）

`a`~`g`_test.go 固化成 `go test` 自动化测试，`make tier1` 一条命令跑完，**23 条全绿**。

- **A/D/E 三组**（Task 19，11 条）：每条测试自己在 `t.TempDir()` 里现搭一个最小 `brickkit.yaml` + 假 `component.yaml`，不依赖装配仓库本身的状态、也不产生任何副作用。
- **B 组**（Task 20，用例 3/4/5/8）：用例 3/4/5 直接对着 `brickkit up` 真实起的完整装配部署做 `docker exec` 断言——`erp-sales` 是全阶段二唯一有真实依赖方的组件，这三条阶段一根本没有验证前提。用例 8 是"`enabled: false` 级联"这条通用机制，用隔离 fixture 验，不扰动真实部署。
- **C 组**（Task 20，用例 11-14）：`local: true` 模式族，隔离 fixture + `--dry-run` 验完，不需要真的在 IDE 里起进程。
- **F 组**（Task 20，用例 18）：`remove` 连 `.archived/` 一起删，用临时的、普通 git 仓库（不是 submodule）里的假组件验——真实装配仓库的 `components/` 全是登记过的 submodule，`sync`/`remove` 撞见会报 `SUBMODULE_GUARD`，那是另一条已知、预期的行为，不是这条断言要验的东西。
- **G 组**（阶段三 Task 15，用例 21-23）：阶段三主线收官后曾经被跳过、拖到系统排查阶段三是否真的做完才补上的三条，见下方「阶段三 Task 15」一节——为什么是 3 条而不是计划原文列的 4 条，同一节有交代。

用例 9 的后半段（brickKit 缓存旁路 bug）与用例 8（`enabled: false` 级联的真实行为）都在写测试过程中推翻过自己最初的假设，细节见下面对应小节。

⚠️ **真实踩过的坑，写这里防止再犯一次**：B 组用例 3/4/5 曾经直接把 `erp-sales` 真实容器的名字（含精确版本号）硬编码进常量，`erp-sales` 升级之后 `docker exec` 找不到旧名字**只会 `t.Skipf`，不会 `FAIL`**——`make tier1` 因此长期"显示全绿"，这三条用例其实从那次升级之后就再没有真的验证过任何东西，直到很久以后系统排查阶段三是否真的做完才被发现。现在改用 `dockerContainerByPrefix`（`helper_test.go`）按名字前缀动态发现容器，不再把具体版本号编进代码——新写需要找真实容器的测试都该用这个函数，不要把版本号写死。

## 怎么跑

```bash
cd tools/be-acceptance
go test ./platform/... -run TestPlatform -v -count=1
```

用例 10 的后半段（`TestPlatform10b`）需要先建一个假镜像（真实网络请求跑不动 45 秒冷启动，得真容器）：

```bash
docker build -t brickenterprise/fixture-slow-start:1.0.0 platform/fixtures/slow-start
```

镜像不在本地时这条测试会自动跳过（不是放宽断言）。这条测试真的要等 45+ 秒，`go test` 默认超时够用，CI 里如果单独跑它要给够时间。

B 组的用例 3/4/5（`TestPlatform03/04/05`）需要装配仓库根目录已经 `brickkit up` 起真实的五组件部署（Task 18），且 `erp-sales` 容器名固定是 `brickkit-be-assembly-standard-erp-sales-1-0-1-1`——版本号变了要跟着改 `b_test.go` 里的常量。用例 18（`TestPlatform18`）需要 `git` 在 PATH 上。这些前提缺失时对应测试会跳过，不是放宽断言。

## 20 条清单

| # | 验什么 | 怎么验 | 对应本书 | 状态 |
|---|---|---|---|---|
| 1 | 未知键当场报错 | 往 `component.yaml` 塞一个 `assembly_role: optional`，期望 `up` 失败 | §3.5 | ✅ `TestPlatform01`：`up --dry-run` 报错，退出码非 0，错误信息点出 `assembly_role` |
| 2 | `assembly.yaml` 靠 `artifacts` 随组件分发 | 声明 `type: metadata`，检查 `.brickkit/artifacts/<服务名>/metadata/assembly.yaml` 在 | §3.5 | ✅ `TestPlatform02`：⚠️ **写这条测试时纠正了一个自己的错误假设**——一开始以为分发发生在 `up` 之后（且容器要真变 healthy），跑完发现 `.brickkit` 下压根没有 `artifacts/` 目录；查 brickKit 源码（`internal/cli/artifacts.go` 的 `downloadArtifacts` 只被 `add.go`/`add_local.go` 调用）才确认**分发只发生在 `brickkit add`**（004 §3.3 步骤 6），跟 `up`/`up --dry-run` 完全无关。改用 `brickkit add foo/bar@1.0.0 --yes` 后测试真实通过 |
| 3 | 地址变量名由 ID 推导、不带版本号 | 断言 `MDM_CUSTOMER_ENDPOINT=http://mdm-customer-1-0-0:8080` | §2.1、§3.4 | ✅ `TestPlatform03`：`docker exec` 进真实 `erp-sales` 容器，`env` 里 `MDM_CUSTOMER_ENDPOINT=http://mdm-customer-1-0-0:8080` 逐字匹配 |
| 4 | 额外端口地址是 `http://` 而不是 `grpc://` | 断言 `MDM_CUSTOMER_GRPC_ENDPOINT` 的值以 `http://` 开头，且组件的 gRPC 客户端剥掉 scheme 后能连上 | §2.1 | ✅ `TestPlatform04`：`MDM_CUSTOMER_GRPC_ENDPOINT=http://mdm-customer-1-0-0:9090`，"剥掉 scheme 后真的能连上"这一半在 Task 17/18 的真故障注入测试与端到端 `CreateOrder→ConfirmOrder→ShipOrder` 全链路里已经反复验证过 |
| 5 | 弱依赖缺失时不注入那个变量 | 把可选依赖从 `brickkit.yaml` 删掉，断言容器里 `env` 里根本没有那个键（不是空串） | §3.6 | ✅ `TestPlatform05`：`erp-sales` 的 `infra/workflow` 弱依赖缺失，容器里 `INFRA_WORKFLOW_ENDPOINT` 这个键根本不存在（不是空串） |
| 6 | 保留变量会被跳过 | 故意起一个 `otelEndpoint` 配置项，断言 `up` 给警告且容器里拿不到值 | §2.7.3 | ✅ `TestPlatform06`：警告文本点名 `OTEL_ENDPOINT` 且说明"已被忽略"，生成的 compose 里确实没有这个变量 |
| 7 | config 数组会被渲染成 `[a b c]` | 断言数组配置项渲染成空格分隔、不可用的形式 | §6.1 | ✅ `TestPlatform07`：`ENABLED_FEATURES=[a b c]`，逐字匹配（Go `%v` 对 slice 的默认格式，不是逗号分隔） |
| 8 | 启停跟着上层走 | 顶层写 `enabled: false`，断言下层跟着不启动；再验"删条目"与"`enabled: false`"不等价 | §3.6、§5.10 | ✅ `TestPlatform08a`+`08b`：⚠️ 第一次写这条测试想当然地假设 `enabled: false` 级联到强依赖方会**报错阻断**，实测发现是**优雅级联**（`exitCode=0`，两个组件都被标"不启动"，理由分别是"显式禁用"/"强依赖不启动"）。`08b` 用完全相同的依赖结构、只把 `enabled: false` 换成"整条不写"，验证出真正的差异：不写条目时只要安装源里能找到，强依赖照样正常解析拉进来启动——两者只有在"真的没人需要它"时结果才相同 |
| 9 | Fork 遮蔽按 `sources` 顺序 | 同 id 同 version 两份源，断言靠前的赢；再验改了 `metadata.id` 之后 `*_ENDPOINT` 消失 | §3.4.1 | ✅ `TestPlatform09a`（靠前的源赢）+ `TestPlatform09b`（改名后 `*_ENDPOINT` 消失）都通过。`09b` 曾经真红过一轮，是一个真实的 brickKit bug——brickKit v0.2.2 修复后重跑变绿，见下方「用例 9 后半段」 |
| 10 | 默认启动宽限是 60 秒 | 造一个冷启动 45 秒的组件，不写 `startPeriodSeconds`，断言它能起来 | §12.3.5 | ✅ `TestPlatform10a`（配置值确实是 60s）+ `TestPlatform10b`（**真造了一个 sleep 45 秒的假组件**，`fixtures/slow-start/`，真机 `brickkit up` 验证它被 60 秒宽限顶住、`RestartCount=0`、真等了 45+ 秒不是被 mock 掉的）。阶段一遗留的"只验配置值没验真扛住"的缺口在本阶段补齐 |
| 11 | `local: true` 的三件事 | 断言不生成 service、依赖方有 `extra_hosts: <服务名>:host-gateway`、注入端口换成 `localPort` | §13.1 | ✅ `TestPlatform11`：三件事在同一个真实生成的 compose 里逐条断言——`foo-bar-1-0-0` 没有自己的 service 块，`baz-qux` 有 `extra_hosts: foo-bar-1-0-0:host-gateway`，`FOO_BAR_ENDPOINT` 用的是 `localPort`（18001）不是组件自己声明的 `deployment.port`（19001） |
| 12 | `local: true` 的额外端口不被改写 | 断言依赖方拿到的 gRPC 地址仍是 Manifest 里声明的端口；再验两个 local 组件撞同一额外端口时 `up` 报错 | §3.5.1.1 | ✅ `TestPlatform12a`（主端口用 `localPort`=18001，额外端口 grpc 维持 Manifest 声明的 19091，两者互不影响）+ `TestPlatform12b`（两个 `local: true` 组件的额外端口都写 19099，`PORT_CONFLICT` 报错） |
| 13 | `local: true` 不生成迁移容器 | 断言 `up` 给警告，且库里表没建 | §13.3 铁律五 | ✅ `TestPlatform13`：警告文本"local 组件的数据库迁移不会自动执行"，生成的 compose 里没有任何 migration 相关内容（`local: true` 组件本身不生成容器，天然不会有迁移容器去建表） |
| 14 | `local: true` + `k8s` 直接报错 | 断言生成阶段失败 | 决策 88 | ✅ `TestPlatform14`：`CONFIG_INVALID`，错误信息点出"local: true 只能在 deploy.target: docker 下使用" |
| 15 | K8s 下 labels 落在 Deployment 与 Pod 的 annotations | `--dry-run` 出清单，两处都断言 | §7.5.1 | ✅ `TestPlatform15`：生成的 Deployment 清单里，顶层 `metadata.annotations` 与 `spec.template.metadata.annotations` 两处都有声明的 label |
| 16 | K8s 下 hostname 必填且唯一 | 两个组件写同一个 hostname，断言硬报错（不是静默） | §6.3 | ✅ `TestPlatform16a`（两组件同 hostname 硬报错）+ `TestPlatform16b`（K8s 下 `expose: true` 漏写 hostname 报错） |
| 17 | `up` 只打印建库语句，不建库 | 断言输出里有 `CREATE DATABASE`，而库并没有被创建 | §2.7.2 | ✅ `TestPlatform17`：`--dry-run` 输出里有 `CREATE DATABASE`+库名，且全程不需要数据库可达（fixture 用的 `host.docker.internal` 在隔离环境里连不上，命令依然成功，间接证明没有真的执行这条 SQL） |
| 18 | `remove` 连 `.archived/` 一起删 | 先 `sync` 归档，再 `remove`，断言归档目录也没了 | §9.4.2 | ✅ `TestPlatform18`：临时假组件（普通 git 仓库，`--force` 跳过"删了找不回来"的安全网），`sync` 归档到 `.archived/`，`remove` 之后归档目录也真的被删干净 |
| 19 | 精确版本 | 写 `^1.2`，断言报错 | 四条铁律之一 | ✅ `TestPlatform19`：`CONFIG_INVALID`，错误信息点出"精确版本" |
| 20 | 本地源不受签名约束 | 开 `requireSignature: true`，断言本地源组件照装 | §9.4.1 | ✅ `TestPlatform20`：`requireSignature: true` + 本地源，`up --dry-run` 照常成功（§8.5.2：只有市场安装源受签名约束） |

## 用例 9 后半段：一个真实的 brickKit bug（已修复，v0.2.2）

**现象**：`baz/qux` 声明弱依赖 `foo/bar@1.0.0`。先跑一次 `up --dry-run`，`baz/qux` 容器正确拿到 `FOO_BAR_ENDPOINT`。**不清理 `.brickkit/` 缓存**，把 `foo/bar` 的 `component.yaml` 改成 `metadata.id: foo/bar2`（目录本身不动），重新跑 `up --dry-run`——v0.2.1 上 **`foo/bar@1.0.0` 仍然被判定为存在**，`FOO_BAR_ENDPOINT` 依然被注入，压根不会触发"弱依赖缺失"的警告。这与设计文档明确写下的行为矛盾（`mdm-customer` 等组件的 `AGENTS.md`：*"Fork 件的 `metadata.id` 与 `version` 一个字都不能改——改了之后所有依赖方拿到的 `*_ENDPOINT` 整个消失"*）。

**排除过的假阳性**：第一次复现时用了双源（`source-a`/`source-b`）fixture，改名后 `source-b` 里未经修改的同 id 副本"顶了上来"，误以为 bug 不存在——那是我自己的 fixture 复用失误，不是 brickKit 的问题。用单源、干净的 `t.TempDir()` 重新隔离验证后，问题真实存在且可稳定复现（见 `TestPlatform09b` 及其详细注释）。

**根因**（v0.2.1 的代码）：`internal/source/source.go` 的 `Client.Manifest()` 里，`servedByLocalSource()` 只回答"某个本地源现在还提供这个 id/version 吗"——改名后的 `foo/bar` 目录文件还在、还能被读到，但内容的 `metadata.id` 不再匹配请求的 `foo/bar`，`servedByLocalSource` 因此返回 `false`；`Manifest()` 把这个 `false` 直接当成"应该走缓存"的信号，退回改名前的旧缓存，没有走到本该正确处理这种情况的 `fetchManifest()`。完整诊断（含逐行代码分析、最小复现脚本）写成报告交给了 brickKit：`brickKit` 仓库 `docs/superpowers/specs/2026-09-08-local-source-manifest-cache-masks-metadata-id-mismatch.md`。

**修复**（brickKit v0.2.2，commit `f8e1fa9`）：`servedByLocalSource` 的判据加一条 `!manifestIDMatches(raw, id)`——id 对不上就返回 `true`（等同"这个源有文件但坏了"），逼 `Manifest()`/`DownloadArtifacts()` 走 `fetchManifest()` 重新判定、报"未找到"。**关键细节**：只看 `id`，不看 `version`——同一个 `id`、本地目录已经升到新版本号时，请求旧版本仍然要能从缓存拿到那份旧 Manifest（多版本共存是正常用法，试用指南 §8.2/§8.6），brickKit 那边的评估把这一点专门收窄说明过，没有把 `metadata.id` 与 `metadata.version` 的不匹配一并处理。

**本仓库的复核**：升级 `~/.local/bin/brickkit` 到 v0.2.2 后，`make tier1` 重新跑，`TestPlatform09b` 转绿；另外单独手工重跑了三段裸 `brickkit` 命令验证（改名后的强依赖报 `COMPONENT_NOT_FOUND`、弱依赖降级为警告且 `*_ENDPOINT` 消失、纯版本升级仍正确从缓存服务旧版本），行为与 brickKit 那边记录的修复结果完全一致，`go test ./...`（brickKit 自己的全部测试）同步跑过一遍确认无回归。

## 用例 8：`enabled: false` 级联到强依赖方是优雅降级，不是报错

第一次写 `TestPlatform08a` 时想当然地假设：顶层组件 `enabled: false`、又被另一个组件强依赖，`brickkit up` 会报错阻断（"强依赖不可获取"）。实测完全不是——`up --dry-run` 的输出是：

```
📋 组件状态计算：
   ⬜ foo/bar@1.0.0  显式禁用（enabled: false）
   ⬜ baz/qux@1.0.0  不启动（强依赖 foo/bar 不启动）

📋 本次没有组件会启动
```

`exitCode` 是 `0`，不是错误——两个组件都被标成"不启动"，各自给出清楚的理由，这是一次优雅的级联降级，不是配置校验失败。

`TestPlatform08b` 用完全相同的 `foo/bar`→`baz/qux` 强依赖结构做对照，只改一个变量：把 `foo/bar` 的顶层条目**整条不写**（不是写 `enabled: false`）。结果是 `foo/bar` 被正常解析、自动拉进来启动，`baz/qux` 也正常拿到 `FOO_BAR_ENDPOINT`——这才是"删条目"与"`enabled: false`"真正不等价的地方：`enabled: false` 是**显式**声明"我知道这个组件存在，但明确不要它启动"，会强制级联断开所有需要它的边；不写条目只是**没有主动要求**它启动，安装源里能找到就照样会被依赖解析拉进来。两者只有在"真的没有任何组件需要它"时结果才相同（都不启动）——导读第 10 条"客户没买的组件写 `enabled: false`……没买的正确写法是整条不写进 `brickkit.yaml`"说的正是这个差异：如果这个组件确实被别的（客户买了的）组件依赖着，写 `enabled: false` 会连累那个依赖方一起停摆，而不写条目不会。
