# be-acceptance

验收测试。**不是 brickKit 组件**——业务闭环 + 拆回门禁 + import 扫描（v0.4.0 起删除了 v0.4 的 `platform/` 平台断言，06f 按 brickKit v1 重写）（总纲 §2.3 非组件资产仓库表）。

## 子目录

| 目录 | 装什么 | 什么时候 |
|---|---|---|
| `closedloop/` | 业务闭环测试：跨组件事件握手、Saga 补偿等端到端场景 | 有第一条跨组件业务流程时 |
| `gates/` | 跨仓库才看得出来的门禁：**铁律六**（组件互不 import）、**拆回门禁**（合并态能不能拆回去） | 铁律六见 Task 9；拆回门禁见阶段四 |

⚠️ **`module-check`（铁律七）不在这里**：那是逐仓库的 grep 检查，落在各组件自己的 `Makefile`（总纲 §I 门禁 9）。`be-acceptance` 只管跨仓库才看得出来的那些。

## 现状（阶段一 Task 9）

`gates/` 的铁律六 import 扫描已实现（`gates.ImportScan` + `cmd/be-acceptance` 的 `gate import-scan` 子命令），用 `go/parser` 解析每个组件目录的 `.go` 文件，命中本组织下、不在 `be-sdk-*` 白名单、也不是自己 module 的 import 就判违规。目前只扫 Go——Python 组件出现前不实现 Python 扫描（没有真实样本可核对 import 路径写法，见 `gates/importscan.go` 的注释）。

`closedloop/` 的业务闭环测试阶段三起逐步实现。

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

## 现状（测试体系扩展，总纲 SOP-W-8）

补第 5、6 个 gate（第 5 个当时漏记本节，一并补上）：

- `data-scope-test-scan`（`gates.DataScopeTestScan`）：声明了真实 `data_scopes` 维度（非 `none`）的组件，
  测试文件里必须至少有一条"越权/超出范围被拒绝"形状的测试——判据是组件级"至少一条"，不是逐维度，精度
  上限见 `gates/datascopetestscan.go` 注释。
- `dependency-version-scan`（`gates.DependencyVersionScan`）：v0.4.0 起缩减为 brickKit v1 自己拦不住的两类
  （实验记录见装配仓库 `dev/test-records/06a/task10-version-checks-experiment.md`）：①外壳
  `shell/be/<name>/go.mod` 锁定的成员版本 vs 对应组件自己的 `metadata.version`（v2+ 的 require 带 `/vN`
  模块路径后缀）；②外壳自己 `metadata.version` vs `deployment.image` 镜像 tag。原先的组件依赖引用、
  `brickkit.yaml` 顶层 pin、`shell.members` 三类漂移，v1 的 `brickkit up --dry-run` 会拦下并给出可操作提示，
  不再重复检查——`make gates` 同时跑 `brickkit up --dry-run`。`bump-version`（v0.4.1 起）同样缩减：只改 `metadata.version` 那一行的版本号、`deployment.image` 与旧版本一致的 tag，并传播下游
  `component.yaml` 的依赖版本、外壳 `shell.members`、外壳 `go.mod`，不再碰 `brickkit.yaml` pin
  （`brickkit upgrade`）、`config:` 主机名字面量（`$var:`）、AGENTS 名册（CLI 维护块）。
  **`bump-version` 不再往 `component.yaml` 里写任何注释**：计划文件里的 `reason` 字段照常必填、照常解析，
  但只在终端原样打印，不落进任何文件、也不进 tag 消息——历史在 git 与 tag 的发布说明里，发布说明手写成一个
  文件（只写上一个 tag 之后的变更）。`deployment.image` 没写（只有 `deployment.build`）时跳过，不报错。
- `bump-version --apply` 打印的收尾步骤按 v1 发布规则分三种：组件是提交并推送 → `brickkit release --notes-file`
  （tag `<ver>`，不带 `v`）→ Go 组件在同一提交上再打 `v<ver>` 并推送 → `brickkit build <id>`；外壳是独立仓库、以子模块
  挂在 `shell/be/<name>/`，在外壳仓库里提交并推送 → `brickkit release --notes-file`（tag `<ver>`，不带 `v`，不打
  `v` 标签）→ `brickkit build <id>` → 回装配仓库提交子模块指针。不打印
  `make image`、不打印 docker push（镜像全部本地使用）。
- `service-hostname-scan`（`gates.ServiceHostnameScan`，第 7 个 gate）：扫 `config/*.yaml` 全文与根目录
  `deploy*.yaml` 的 `vars:` 段（跳过注释），找出版本化服务名（`http://<scope>-<name>-<x>-<y>-<z>`，brickKit
  `ServiceName` 规则），逐个对照 `brickkit.yaml` 的 `components:`：这个组件已声明、但没有这个版本 → 错误，点名
  `文件:行`、主机名和应写的服务名；`brickkit.yaml` 里根本没有这个组件 → 只警告（还没装上它时是预期状态）。
  `$var:` 把字面量集中到了一处，但没有别的东西拿它跟组件当前版本比（lint、`up --dry-run`、`bump-version`
  都不看配置值）——authz/iam 一发版，`AUTHZ_BUNDLE_URL`/`IAM_JWKS_URL` 就过期，全部受保护路由悄悄 503。

## 为什么这条门禁要在档 0 之前就装好

设计书决策 91：前五条铁律破了当场起不来，一小时能修；**铁律六破了没有任何症状**，系统跑得更快了，直到某天要上 K8s 全拆才发现拆不动，那时的代价是重写。装晚一天，就多一天没人看着。
