# 探针契约

验收用的通信探针：`test/probe-go`（`go/`）与 `test/probe-py`（`py/`）实现同一份契约，没有业务逻辑，测出的差异只来自语言、拓扑与引擎（装配项目实验计划的“探针组件”）。改契约时两个探针一起改，`conformance/` 的测试两边都跑。

## 变体

同一份代码以不同的组件 ID 运行，使一个外壳能承载多个探针、两个探针能在同一进程里互相调用。变体的目录（`go2/`、`go3/`、`go4/`、`py2/`）由 `variants.py` 生成，只有清单、文档与按发布标签构建的 `Dockerfile`；入口按 `COMPONENT_ID` 选变体，外壳编进全部变体。

| 组件 ID | 代码 | HTTP | gRPC | 事件流 |
| --- | --- | --- | --- | --- |
| `test/probe-go` | `go/` | 8600 | 9600 | `test.probe_go` |
| `test/probe-go2`、`-go3`、`-go4` | `go/` | 8602、8603、8604 | 9602、9603、9604 | `test.probe_go2` 等 |
| `test/probe-py` | `py/` | 8601 | 9601 | `test.probe_py` |
| `test/probe-py2` | `py/` | 8605 | 9605 | `test.probe_py2` |

下文的 `<前缀>` 是组件 ID 中的 `/` 换成 `.`、`-` 换成 `_`（`test/probe-go2` → `test.probe_go2`），每个变体拥有自己的资源类型与操作键。

## 用户面（HTTP，主端口）

`PROBE_GUARD` 为假（缺省）时这些路由是公开的；为真时每条路由需要用户凭据与表中的操作（授权视图来自授权组件）。

| 方法与路径 | 请求 | 回答 | 操作 |
| --- | --- | --- | --- |
| `POST /api/probe/echo` | `{"payload": "<字符串>"}` | 原样回送 | `<前缀>.echo` |
| `POST /api/probe/work` | `{"ms": 0..1000}` | 消耗这么多毫秒的 CPU 后 `{"ms": n}`；不阻塞同一进程里的其他请求（Go 在请求自己的 goroutine 里做，Python 在工作进程里做：外壳里成员共用一个事件循环，线程仍争 GIL；Python 3.2.28 起） | `<前缀>.work` |
| `POST /api/probe/write` | `{"bytes": 0..524288, "relay": 布尔}`；3.2.23（Go）、3.2.22（Python）起可选 `"label"`（至多 64 字节）与 `"event": "noted"`；3.2.29（Go；Python 探针随底层先行第 6 项补齐）起可选 `"fail": 布尔` 与 `"hold_ms": 0..2000` | 一个事务内写一行并发一个事件，`201 {"id"}`；载荷超过 64 KiB 走对象存储的凭证检查（大事件）；`"event": "noted"` 时发 `test.probe.noted.v1` 而不是 `written`；`fail` 为真时事件发出后整个事务回滚，答 400 `REQUEST_INVALID`；`hold_ms` 让事务在发出事件后再开着这么久（套件的事件剖面用） | `<前缀>.write` |
| `POST /api/probe/call` | `{"op": "echo"\|"work"\|"note"（`note` 自同上的版本起）, "surface": "grpc"\|"http", "bytes", "ms"}` | 调用下一个探针的同一操作（`PROBE_NEXT` 是它的组件 ID，`PROBE_NEXT_URL`、`PROBE_NEXT_GRPC_URL` 是它的两个地址，部署用 `$endpoint:<ID>` 与 `$endpoint:<ID>:grpc` 填入，协议 P4.26），`{"ok": true, "elapsed_us": n}`：`elapsed_us` 是调用方自己测的这一跳（从发出到收到回答），不含驱动到调用方的那一跳；没有下一个探针答 409 `NO_NEXT_PROBE` | `<前缀>.call` |
| `GET /api/probe/seen` | — | `{"count", "big", "relayed", "noted"}`：收到的对方事件数、其中超过 64 KiB 的数、已转发成调用的数、其中 `noted` 类型的数（自同上的版本起） | `<前缀>.read` |
| `POST /api/probe/note` | `{"payload": "<字符串>"}` | `{"payload": "noted:<字符串>"}`（自同上的版本起） | `<前缀>.note` |
| `GET /api/probe/written/{id}` | — | `{"id", "bytes", "label"}`：读回写入的一行，旧版探针写的行 `label` 为空（自同上的版本起） | `<前缀>.read` |

## 系统面（gRPC，`grpc` 端口，mTLS）

| 方法 | 请求与回答 | 操作 |
| --- | --- | --- |
| `/test.probe.v1.Probe/Echo` | `google.protobuf.BytesValue`，原样回送 | `<前缀>.echo` |
| `/test.probe.v1.Probe/Work` | `google.protobuf.UInt32Value`（毫秒，至多 1000） | `<前缀>.work` |
| `/test.probe.v1.Probe/Note` | `google.protobuf.BytesValue`，回答前面加上 `noted:`（自同上的版本起；旧版没有这个方法） | `<前缀>.note` |

允许的调用组件：全部探针（六个组件 ID）。

标准系统方法另由 SDK 挂上（不在上表）：补发 `be.source.v1.ResendService`（`<前缀>.resend`）；整套恢复的核对 `be.recovery.v1.CheckService` 与外部对账 `be.external.v1.ReconcileService`（`<前缀>.recover`，只许恢复工具 `ops/recover`；3.2.42 起，Go；Python 探针随底层先行第 6 项）。

## 事件

每个探针向自己的流发 `test.probe.written.v1`（见变体表，各 4 个分区），数据 `{"id", "relay", "blob"}`，有 `label` 时加上它；写入时要求 `noted` 的发 `test.probe.noted.v1`，数据相同。`PROBE_PEER_STREAM` 指向对方的流时消费它（两种类型；旧版只订阅 `written`，`noted` 由运输过滤掉，协议 P6.33）：记一行与它的类型；`relay` 为真时在事务之外调用 `PROBE_NEXT` 的 `Echo`（1 KiB）；数据里 `emit` 为真时（3.2.29 起，Go）在处理的事务里向自己的流再发一个 `written`（跳数比收到的多一，超过 10 时发不出、处理失败，协议 P6.15）。Go 探针用 SDK 的后台工作项，Python 探针用自己的转发队列（Python SDK 子集没有后台工作项；认领规则相同：`FOR UPDATE SKIP LOCKED`、租约到期再认领、每 0.5 秒轮询、并发 4）。

## 后台工作项（3.2.30 起，Go；Python 探针随底层先行第 6 项补齐）

给套件的后台工作剖面用（`conformance/profile/work.py`）。两条用户面路由的操作都是 `<前缀>.write`：

| 方法与路径 | 请求 | 回答 |
| --- | --- | --- |
| `POST /api/probe/job` | `{"effect": "local"（缺省）\|"idempotent_external"\|"non_idempotent_external", "hold_ms": 0..120000, "fail": ""\|"retryable"\|"permanent", "fail_times": n, "rollback": 布尔}` | 一个事务里写一行 `probe_job_requests` 并排入作业 `probe.job.local`、`probe.job.idempotent` 或 `probe.job.non_idempotent`（按副作用种类，各至多 3 次尝试），`201 {"id": <工作项身份>}`；`rollback` 时排入后整个事务回滚，答 400 |
| `POST /api/probe/steps` | `{"n": 1..20, "hold_step": 0..n}` | 给 singleton 作业 `probe.steps` 下一次运行的任务，`202`；只有配置了 `PROBE_STEPS_EVERY`（时长，缺省 0：不声明这个作业；两种探针的清单都声明这个键，Python 探针 3.2.32 起声明、暂不读）时它才运行 |

一次尝试：先在 `probe_job_attempts` 记一行（工作项、探针自己数的第几次、`operation_id`、本进程的身份 `instance`、开始时刻），结束时写 `ended`（`done`、`failed`、`cancelled`；进程死在里面时为空）。`fail` 为 `retryable` 时前 `fail_times` 次答可重试的错，`permanent` 时第一次就答不可重试的错，用完或不可重试时死信处理在 `probe_job_dead` 记一行。外部作业先“调用对方”，对方是 `probe_job_calls`：幂等的按 `Run.SubKey("call")` 每个键只收一次，非幂等的先 `BeforeCall`、按 `operation_id` 每次都收。第一次尝试在提交效果之前（外部作业在调用之后）等 `hold_ms`，让套件在这期间停止、杀掉探针或推进恢复代次；以后的尝试不等。效果与完成标记在同一个事务里：`probe_job_effects` 记工作项、步、`operation_id` 与 `instance`。

配置了 `PROBE_CRON`（3.2.32 起，Go；两个探针的清单都声明这个键）时另有 cron 作业 `probe.minutely`（每分钟一槽，UTC），每次运行在 `probe_cron` 记一行（槽与 `instance`，不加唯一约束：同一个槽跑了两次会显出来）。

`probe.steps` 每次运行读 `probe_steps` 的任务，没有就完成；有就从已提交的进度之后一步一段：每步记一次尝试，用 `Checkpoint` 把这一步的效果与进度一起提交，最后一步用 `Complete` 并删掉任务。第一个走到 `hold_step` 的进程在那一步等（至多 120 秒），套件趁机杀掉它。

## 水位来源（3.2.31 起，Go；Python 探针随底层先行第 6 项补齐）

每个 Go 变体另有一条水位来源的流 `<事件流>_wm`（2 个分区；`test.probe_go_wm` 等），给套件的水位来源剖面用（`conformance/profile/watermark.py`）。`POST /api/probe/write` 带 `"watermark": true` 时事件发到这条流，`besubjects` 为 `["probe:<写入的身份>"]`。补取方法 `be.source.v1.WatermarkService/Pull` 的操作是 `<前缀>.pull`，补取者是全部探针与套件自己的身份 `test/acceptance`。

## 付款：对外命令（3.2.41 起，Go；Python 探针随底层先行第 6 项补齐）

给套件的对外命令剖面用（`conformance/tests/test_external_profile.py`，假外部系统 `conformance/external/fake.py`；线协议第 13 章，装配项目协议 10）。付款是一条对外命令，经 SDK 的 `extcmd` 发到 `PROBE_EXTERNAL_URL` 的假外部系统；业务参考号就是外部的幂等键。声明的能力按配置：`PROBE_EXTERNAL_DEDUPE_WINDOW`（去重保留期，0 为不去重）、`PROBE_EXTERNAL_LAST_USE`（按最后使用计期）、`PROBE_EXTERNAL_MAX_PROCESSING`（最长处理时间）、`PROBE_EXTERNAL_IRREVOCABLE`（不可撤回，缺省真：发出前把发出记录交给证据存储，`AUDIT_GRPC_URL`；没有证据存储就不发）。没有 `PROBE_EXTERNAL_URL` 时付款发不出去（按“肯定没到达”退避）。两个探针的清单一样：Python 探针声明这几个键与 `EXTERNAL_COMMANDS_QUERY_ONLY` 但不读（A144）。适配器（3.2.43 起）还能按时间窗列出假外部系统收到的付款（`GET /payments?from=&to=`），整套恢复的外部对账用一次列出代替逐条查询（线协议 P13.11）；假外部系统也可以单独起（`python3 fake.py --port N`，`POST /control` 设故障，套件 1.0.55 起可以只对一个参考号设，`faults_by_reference`），演练用。

| 方法与路径 | 请求 | 回答 | 操作 |
| --- | --- | --- | --- |
| `POST /api/probe/payment` | `{"reference", "params": {…}, "expires_at"?}` | `{"command", "state"}`：在这个请求的事务里写命令；同一参考号、同样参数答原命令，参数不同答 409 `IDEMPOTENCY_KEY_REUSED` | `<前缀>.pay` |
| `GET /api/probe/payment/{reference}` | — | `{"command", "state", "reason", "external_id", "resend_deadline", "attempts": [{"kind", "result", "reached"}]}` | `<前缀>.read` |

## 授权剖面的条目（3.2.38 起，两种探针）

给套件的授权剖面用（`conformance/profile/authz.py`，CK-AZ-01..05）。只在 `PROBE_GUARD` 为真时注册，路由都受保护。资源 `<前缀>.entry` 每种维度一个：`legal_entity`（值集合，列 `legal_entity`）、`dept`（组织树，列 `dept_path`）、`owner`（主体，列 `owner_id`，角色 `owner`）、`amount`（金额，列 `amount` 与 `currency`）；字段 `id`、`number`、`legal_entity`、`dept_path`、`owner_id`、`amount`、`currency`、`approved` 普通，`cost` 受限（读取键 `<前缀>.entry.cost`），`note` 秘密。列为 SQL NULL 是已知的 null。

| 方法与路径 | 请求 | 回答 | 操作 |
| --- | --- | --- | --- |
| `GET /api/probe/entries` | 查询参数：`number`、`legal_entity`、`dept_path`、`owner_id`、`amount`、`currency`、`cost` 任一为等值筛选；`sort=<字段>` 或 `-<字段>`（`number`、`cost`）；`readable_only=<字段>,…`；`limit`、`cursor` | `{"items", "next", "readable_only"?}`：每行按命中段的字段掩码（P8.31：不可读的字段为 `null` 并列在 `_masked`，秘密字段不出现） | `<前缀>.entry.read` |
| `GET /api/probe/entries/{id}` | — | 单对象，同样掩码；看不见答 404 | `<前缀>.entry.read` |
| `POST /api/probe/entries` | `{"number", "legal_entity", "dept_path", "owner_id", "amount", "currency", "cost", "note"}`（维度可为 `null`） | `201 {"id"}`；候选的新状态不在授权内答 403 `PERMISSION_DENIED` | `<前缀>.entry.create`（`create`） |
| `POST /api/probe/entries/{id}/move` | 维度的新值（给出的才改） | 204；原状态先按读取与动作判断（看不见 404），新状态不在授权内答 403 `PERMISSION_DENIED`，条目不变 | `<前缀>.entry.move` |
| `POST /api/probe/entries/{id}/approve` | `{"hold_ms": 0..2000}` | 204；高风险（`W_high`）；写入后事务再开着 `hold_ms`，提交前视图已过期则回滚并答 503 `AUTHORIZATION_VIEW_EXPIRED`（P8.12） | `<前缀>.entry.approve`（高风险） |
| `GET /api/probe/entries/{id}/export` | — | 单对象，按导出操作自己的段掩码（P8.15） | `<前缀>.entry.export`（`export`） |

`PROBE_UNDECLARED_ROUTE` 为真（且 `PROBE_GUARD` 为真）时另注册 `GET /api/probe/undeclared`，操作 `<前缀>.undeclared` 没有声明，启动失败（CK-AZ-01）。

## 资源与操作

资源 `<前缀>`（无维度），查看操作 `<前缀>.read`，动作 `<前缀>.echo`、`work`、`write`、`call`、`note`（自同上的版本起）、`pull`（3.2.31 起，Go：水位来源的补取）、`pay`（3.2.41 起，Go：付款），风险均为低。

## 迁移

`0001_probe`：`probe_writes`、`probe_seen`、`probe_relay`（Python）。`0002_label`（扩展，自同上的版本起）：`probe_writes.label`、`probe_seen.type`，都可空；旧版探针在加了这两列的结构上照常读写。`0003_bytes`（收缩，3.2.24（Go）、3.2.23（Python）起）：`probe_writes.size` 改名为 `bytes`；更早的探针在这个结构上写入会失败，只用于套件 D 的反例。`0004_received_by`（扩展，3.2.26（Go）、3.2.25（Python）起）：`probe_seen.received_by`，可空，探针不读写它；只为套件 D 在新 SDK 上复测 V1。`0005_jobs`（扩展，3.2.30 起，Go）：后台工作项一节的 `probe_job_requests`、`probe_job_attempts`、`probe_job_effects`、`probe_job_calls`、`probe_job_dead`、`probe_steps`。`0006_cron`（扩展，3.2.32 起，Go）：`probe_cron`。`probe_entries`（扩展，3.2.38 起）：授权剖面的条目；Go 探针是 `0008_entries`，Python 探针是 `0005_entries`（Python 探针还没有后台工作项、cron 与水位来源的迁移，编号各走各的）。
