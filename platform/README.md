# platform —— 档 1 平台验收清单（设计书 §9.6.2）

这批用例的被测对象**是 brickKit 本身，不是我们的业务**。每一条对应设计书某处断言，跑一次就知道那处断言还成不成立——**平台升级后重跑，就是回归测试**。

⚠️ **这不是占位符，是任务清单。** 发现平台真有问题时，先在这里记一条用例（哪怕是红的），再回 brickKit 仓库修——不许在业务侧绕过去。绕过去的那一条，会在 62 个组件时变成 61 处绕法（设计书 §9.6.2）。

## 现状（阶段二 Task 19）

A/E/D 三组共 11 条已经固化成 `go test` 自动化测试（`a_test.go`/`e_test.go`/`d_test.go`），跑法见下方「怎么跑」。每条测试自己在 `t.TempDir()` 里现搭一个最小 `brickkit.yaml` + 假 `component.yaml`，不依赖装配仓库本身的状态、也不产生任何副作用——平台断言测的是 brickKit 的行为，不该跟着业务组件的迭代节奏一起变。

**11 条全绿。** 用例 9 的后半段曾经真红过一轮——过程中发现了一个真实的 brickKit bug（不是测试写错，已经排除"复用了别的 fixture 状态"这种假阳性），已经写成报告交给 brickKit 那边评估：`docs/superpowers/specs/2026-09-08-local-source-manifest-cache-masks-metadata-id-mismatch.md`（brickKit 仓库）。brickKit v0.2.2（commit `f8e1fa9`）修复后，本仓库升级 CLI 到 v0.2.2 并重新跑过 `TestPlatform09b`，真绿——细节见下面用例 9 那一行。

B/C/F 三组（用例 3、4、5、8、11-14、18）留给 Task 20，需要真组件（`erp-sales` 的依赖树）。

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

## 20 条清单

| # | 验什么 | 怎么验 | 对应本书 | 状态 |
|---|---|---|---|---|
| 1 | 未知键当场报错 | 往 `component.yaml` 塞一个 `assembly_role: optional`，期望 `up` 失败 | §3.5 | ✅ `TestPlatform01`：`up --dry-run` 报错，退出码非 0，错误信息点出 `assembly_role` |
| 2 | `assembly.yaml` 靠 `artifacts` 随组件分发 | 声明 `type: metadata`，检查 `.brickkit/artifacts/<服务名>/metadata/assembly.yaml` 在 | §3.5 | ✅ `TestPlatform02`：⚠️ **写这条测试时纠正了一个自己的错误假设**——一开始以为分发发生在 `up` 之后（且容器要真变 healthy），跑完发现 `.brickkit` 下压根没有 `artifacts/` 目录；查 brickKit 源码（`internal/cli/artifacts.go` 的 `downloadArtifacts` 只被 `add.go`/`add_local.go` 调用）才确认**分发只发生在 `brickkit add`**（004 §3.3 步骤 6），跟 `up`/`up --dry-run` 完全无关。改用 `brickkit add foo/bar@1.0.0 --yes` 后测试真实通过 |
| 3 | 地址变量名由 ID 推导、不带版本号 | 断言 `MDM_CUSTOMER_ENDPOINT=http://mdm-customer-1-0-0:8080` | §2.1、§3.4 | ⏳ Task 20（B 组，需要真组件） |
| 4 | 额外端口地址是 `http://` 而不是 `grpc://` | 断言 `MDM_CUSTOMER_GRPC_ENDPOINT` 的值以 `http://` 开头，且组件的 gRPC 客户端剥掉 scheme 后能连上 | §2.1 | ⏳ Task 20（B 组）——阶段二 `erp-sales` 已经是 `mdm-customer` 的真实依赖方，验证前提已具备 |
| 5 | 弱依赖缺失时不注入那个变量 | 把可选依赖从 `brickkit.yaml` 删掉，断言容器里 `env` 里根本没有那个键（不是空串） | §3.6 | ⏳ Task 20（B 组）——`erp-sales` 的 `infra/workflow` 弱依赖天然是这个场景，Task 18 `brickkit up` 时已经在日志里见过一次警告，还没固化成断言 |
| 6 | 保留变量会被跳过 | 故意起一个 `otelEndpoint` 配置项，断言 `up` 给警告且容器里拿不到值 | §2.7.3 | ✅ `TestPlatform06`：警告文本点名 `OTEL_ENDPOINT` 且说明"已被忽略"，生成的 compose 里确实没有这个变量 |
| 7 | config 数组会被渲染成 `[a b c]` | 断言数组配置项渲染成空格分隔、不可用的形式 | §6.1 | ✅ `TestPlatform07`：`ENABLED_FEATURES=[a b c]`，逐字匹配（Go `%v` 对 slice 的默认格式，不是逗号分隔） |
| 8 | 启停跟着上层走 | 顶层写 `enabled: false`，断言下层跟着不启动；再验"删条目"与"`enabled: false`"不等价 | §3.6、§5.10 | ⏳ Task 20（B 组） |
| 9 | Fork 遮蔽按 `sources` 顺序 | 同 id 同 version 两份源，断言靠前的赢；再验改了 `metadata.id` 之后 `*_ENDPOINT` 消失 | §3.4.1 | ✅ `TestPlatform09a`（靠前的源赢）+ `TestPlatform09b`（改名后 `*_ENDPOINT` 消失）都通过。`09b` 曾经真红过一轮，是一个真实的 brickKit bug——brickKit v0.2.2 修复后重跑变绿，见下方「用例 9 后半段」 |
| 10 | 默认启动宽限是 60 秒 | 造一个冷启动 45 秒的组件，不写 `startPeriodSeconds`，断言它能起来 | §12.3.5 | ✅ `TestPlatform10a`（配置值确实是 60s）+ `TestPlatform10b`（**真造了一个 sleep 45 秒的假组件**，`fixtures/slow-start/`，真机 `brickkit up` 验证它被 60 秒宽限顶住、`RestartCount=0`、真等了 45+ 秒不是被 mock 掉的）。阶段一遗留的"只验配置值没验真扛住"的缺口在本阶段补齐 |
| 11 | `local: true` 的三件事 | 断言不生成 service、依赖方有 `extra_hosts: <服务名>:host-gateway`、注入端口换成 `localPort` | §13.1 | ⏳ Task 20（C 组） |
| 12 | `local: true` 的额外端口不被改写 | 断言依赖方拿到的 gRPC 地址仍是 Manifest 里声明的端口；再验两个 local 组件撞同一额外端口时 `up` 报错 | §3.5.1.1 | ⏳ Task 20（C 组） |
| 13 | `local: true` 不生成迁移容器 | 断言 `up` 给警告，且库里表没建 | §13.3 铁律五 | ⏳ Task 20（C 组） |
| 14 | `local: true` + `k8s` 直接报错 | 断言生成阶段失败 | 决策 88 | ⏳ Task 20（C 组） |
| 15 | K8s 下 labels 落在 Deployment 与 Pod 的 annotations | `--dry-run` 出清单，两处都断言 | §7.5.1 | ✅ `TestPlatform15`：生成的 Deployment 清单里，顶层 `metadata.annotations` 与 `spec.template.metadata.annotations` 两处都有声明的 label |
| 16 | K8s 下 hostname 必填且唯一 | 两个组件写同一个 hostname，断言硬报错（不是静默） | §6.3 | ✅ `TestPlatform16a`（两组件同 hostname 硬报错）+ `TestPlatform16b`（K8s 下 `expose: true` 漏写 hostname 报错） |
| 17 | `up` 只打印建库语句，不建库 | 断言输出里有 `CREATE DATABASE`，而库并没有被创建 | §2.7.2 | ✅ `TestPlatform17`：`--dry-run` 输出里有 `CREATE DATABASE`+库名，且全程不需要数据库可达（fixture 用的 `host.docker.internal` 在隔离环境里连不上，命令依然成功，间接证明没有真的执行这条 SQL） |
| 18 | `remove` 连 `.archived/` 一起删 | 先 `sync` 归档，再 `remove`，断言归档目录也没了 | §9.4.2 | ⏳ Task 20（F 组，用临时假组件验，不用真 submodule） |
| 19 | 精确版本 | 写 `^1.2`，断言报错 | 四条铁律之一 | ✅ `TestPlatform19`：`CONFIG_INVALID`，错误信息点出"精确版本" |
| 20 | 本地源不受签名约束 | 开 `requireSignature: true`，断言本地源组件照装 | §9.4.1 | ✅ `TestPlatform20`：`requireSignature: true` + 本地源，`up --dry-run` 照常成功（§8.5.2：只有市场安装源受签名约束） |

## 用例 9 后半段：一个真实的 brickKit bug（已修复，v0.2.2）

**现象**：`baz/qux` 声明弱依赖 `foo/bar@1.0.0`。先跑一次 `up --dry-run`，`baz/qux` 容器正确拿到 `FOO_BAR_ENDPOINT`。**不清理 `.brickkit/` 缓存**，把 `foo/bar` 的 `component.yaml` 改成 `metadata.id: foo/bar2`（目录本身不动），重新跑 `up --dry-run`——v0.2.1 上 **`foo/bar@1.0.0` 仍然被判定为存在**，`FOO_BAR_ENDPOINT` 依然被注入，压根不会触发"弱依赖缺失"的警告。这与设计文档明确写下的行为矛盾（`mdm-customer` 等组件的 `AGENTS.md`：*"Fork 件的 `metadata.id` 与 `version` 一个字都不能改——改了之后所有依赖方拿到的 `*_ENDPOINT` 整个消失"*）。

**排除过的假阳性**：第一次复现时用了双源（`source-a`/`source-b`）fixture，改名后 `source-b` 里未经修改的同 id 副本"顶了上来"，误以为 bug 不存在——那是我自己的 fixture 复用失误，不是 brickKit 的问题。用单源、干净的 `t.TempDir()` 重新隔离验证后，问题真实存在且可稳定复现（见 `TestPlatform09b` 及其详细注释）。

**根因**（v0.2.1 的代码）：`internal/source/source.go` 的 `Client.Manifest()` 里，`servedByLocalSource()` 只回答"某个本地源现在还提供这个 id/version 吗"——改名后的 `foo/bar` 目录文件还在、还能被读到，但内容的 `metadata.id` 不再匹配请求的 `foo/bar`，`servedByLocalSource` 因此返回 `false`；`Manifest()` 把这个 `false` 直接当成"应该走缓存"的信号，退回改名前的旧缓存，没有走到本该正确处理这种情况的 `fetchManifest()`。完整诊断（含逐行代码分析、最小复现脚本）写成报告交给了 brickKit：`brickKit` 仓库 `docs/superpowers/specs/2026-09-08-local-source-manifest-cache-masks-metadata-id-mismatch.md`。

**修复**（brickKit v0.2.2，commit `f8e1fa9`）：`servedByLocalSource` 的判据加一条 `!manifestIDMatches(raw, id)`——id 对不上就返回 `true`（等同"这个源有文件但坏了"），逼 `Manifest()`/`DownloadArtifacts()` 走 `fetchManifest()` 重新判定、报"未找到"。**关键细节**：只看 `id`，不看 `version`——同一个 `id`、本地目录已经升到新版本号时，请求旧版本仍然要能从缓存拿到那份旧 Manifest（多版本共存是正常用法，试用指南 §8.2/§8.6），brickKit 那边的评估把这一点专门收窄说明过，没有把 `metadata.id` 与 `metadata.version` 的不匹配一并处理。

**本仓库的复核**：升级 `~/.local/bin/brickkit` 到 v0.2.2 后，`make tier1` 重新跑，`TestPlatform09b` 转绿；另外单独手工重跑了三段裸 `brickkit` 命令验证（改名后的强依赖报 `COMPONENT_NOT_FOUND`、弱依赖降级为警告且 `*_ENDPOINT` 消失、纯版本升级仍正确从缓存服务旧版本），行为与 brickKit 那边记录的修复结果完全一致，`go test ./...`（brickKit 自己的全部测试）同步跑过一遍确认无回归。
