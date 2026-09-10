# be-acceptance

验收测试。**不是 brickKit 组件**——20 条平台验收 + 业务闭环 + 拆回门禁 + import 扫描（总纲 §2.3 非组件资产仓库表）。

## 三个子目录

| 目录 | 装什么 | 什么时候 |
|---|---|---|
| `platform/` | 平台验收 20 条：brickKit 自己的行为断言（§9.6.2 档 0）——我们既是它的作者也是第一个真实用户 | 阶段一起逐步补 |
| `closedloop/` | 业务闭环测试：跨组件事件握手、Saga 补偿等端到端场景 | 有第一条跨组件业务流程时 |
| `gates/` | 跨仓库才看得出来的门禁：**铁律六**（组件互不 import）、**拆回门禁**（合并态能不能拆回去） | 铁律六见 Task 9；拆回门禁见阶段四 |

⚠️ **`module-check`（铁律七）不在这里**：那是逐仓库的 grep 检查，落在各组件自己的 `Makefile`（总纲 §I 门禁 9）。`be-acceptance` 只管跨仓库才看得出来的那些。

## 现状（阶段一 Task 9）

`gates/` 的铁律六 import 扫描已实现（`gates.ImportScan` + `cmd/be-acceptance` 的 `gate import-scan` 子命令），用 `go/parser` 解析每个组件目录的 `.go` 文件，命中本组织下、不在 `be-sdk-*` 白名单、也不是自己 module 的 import 就判违规。目前只扫 Go——Python 组件出现前不实现 Python 扫描（没有真实样本可核对 import 路径写法，见 `gates/importscan.go` 的注释）。

`platform/` 与 `closedloop/` 仍只是任务清单（各 20 条 / 13 条），阶段二、三分别实现。

## 现状（阶段三 Task 3）

`system-client-scan`（`gates.SystemClientScan`）与 `bare-route-scan`（`gates.BareRouteScan`，阶段二叫
`bare-gin-scan`/`BareGinScan`，本阶段改名——它现在不只扫 gin）从 Go-only 扩展到 Python/TS，本阶段第一次出现
这两种语言的组件：

- **Python**：危险目录是 `backend/app/http`、`backend/app/grpc`（Go 的 `backend/internal/http`/`grpc` 对应
  位置，阶段三新拍板，见总纲 SOP-B）。没有 `go/ast` 可用，退化成逐行正则——`system_client(` 识别 SystemClient
  误用；`@router.get(...)` 这类装饰器 + `.add_api_route(` 识别裸路由（判据只认装饰器/`add_api_route` 形态，
  不认任意 `.get(`——`dict.get()`/配置读取里的 `.get(` 极常见，纯文本匹配会把它们全部误判）。
- **TS**：危险目录是 `src/resolvers`（`infra-bff-mobile` 的 GraphQL resolver map 存放约定，这个目录不放
  别的东西）。`systemClient(` 识别 SystemClient 误用；resolver 字段的值不是以 `requirePermission(` 开头、
  又长得像函数（箭头函数/`function` 关键字）就判裸 resolver——GraphQL 场景没有"裸路由"这个概念，判据是
  阶段三 Task 3 重新设计的，不是照抄 Go 版。

两条门禁都是"目录约定 + 语言相应的语法识别"，互不需要显式判断组件是什么语言——一个组件只会真的落在
三套目录约定中的一套。已知精度上限记在各自源文件的注释里（`gates/systemclientscan.go`、
`gates/bareginscan.go`）。

## 现状（阶段三收官后）

新增第 4 个 gate `events-breaking-scan`（`gates.EventsBreakingScan` + `gate events-breaking-scan` 子命令）。
`buf breaking` 只认 `.proto`，`contracts/events/*.json` 的"只增不删不改"（设计书 §3.10、决策 19）此前
完全没有机器门禁（总纲 SOP-W-8）。这个 gate 把每个组件的 events JSON 工作区版本与其 git `main` 版本
各自拍平成一组签名（`event::<subject>` 存在性 + `<字段路径>::type=<t>`），`main` 里的签名少一条就报违规
——删字段、改类型、删 subject 都会让某条旧签名消失；追加新字段/新事件只产生新签名，不触发。拿不到
`main` 基线（新文件/无 main ref/非 git 仓库）就跳过那个文件，同 `buf breaking` 无 `.git` 时的行为。
没写成通用 JSON Schema diff——events JSON 是自定义的 envelope+events 结构，专门写一个反而更准更简单。

## 为什么这条门禁要在档 0 之前就装好

设计书决策 91：前五条铁律破了当场起不来，一小时能修；**铁律六破了没有任何症状**，系统跑得更快了，直到某天要上 K8s 全拆才发现拆不动，那时的代价是重写。装晚一天，就多一天没人看着。
