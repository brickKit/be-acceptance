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
| `POST /api/probe/work` | `{"ms": 0..1000}` | 消耗这么多毫秒的 CPU 后 `{"ms": n}` | `<前缀>.work` |
| `POST /api/probe/write` | `{"bytes": 0..524288, "relay": 布尔}` | 一个事务内写一行并发一个事件，`201 {"id"}`；载荷超过 64 KiB 走对象存储的凭证检查（大事件） | `<前缀>.write` |
| `POST /api/probe/call` | `{"op": "echo"\|"work", "surface": "grpc"\|"http", "bytes", "ms"}` | 调用下一个探针的同一操作（`PROBE_NEXT` 是它的组件 ID，`PROBE_NEXT_URL`、`PROBE_NEXT_GRPC_URL` 是它的两个地址，部署用 `$endpoint:<ID>` 与 `$endpoint:<ID>:grpc` 填入，协议 P4.26），`{"ok": true, "elapsed_us": n}`：`elapsed_us` 是调用方自己测的这一跳（从发出到收到回答），不含驱动到调用方的那一跳；没有下一个探针答 409 `NO_NEXT_PROBE` | `<前缀>.call` |
| `GET /api/probe/seen` | — | `{"count", "big", "relayed"}`：收到的对方事件数、其中超过 64 KiB 的数、已转发成调用的数 | `<前缀>.read` |

## 系统面（gRPC，`grpc` 端口，mTLS）

| 方法 | 请求与回答 | 操作 |
| --- | --- | --- |
| `/test.probe.v1.Probe/Echo` | `google.protobuf.BytesValue`，原样回送 | `<前缀>.echo` |
| `/test.probe.v1.Probe/Work` | `google.protobuf.UInt32Value`（毫秒，至多 1000） | `<前缀>.work` |

允许的调用组件：全部探针（六个组件 ID）。

## 事件

每个探针向自己的流发 `test.probe.written.v1`（见变体表，各 4 个分区），数据 `{"id", "relay", "blob"}`。`PROBE_PEER_STREAM` 指向对方的流时消费它：记一行；`relay` 为真时在事务之外调用 `PROBE_NEXT` 的 `Echo`（1 KiB）。Go 探针用 SDK 的后台工作项，Python 探针用自己的转发队列（Python SDK 子集没有后台工作项；认领规则相同：`FOR UPDATE SKIP LOCKED`、租约到期再认领、每 0.5 秒轮询、并发 4）。

## 资源与操作

资源 `<前缀>`（无维度），查看操作 `<前缀>.read`，动作 `<前缀>.echo`、`work`、`write`、`call`，风险均为低。
