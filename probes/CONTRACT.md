# 探针契约

验收用的通信探针：`test/probe-go`（`go/`）与 `test/probe-py`（`py/`）实现同一份契约，没有业务逻辑，测出的差异只来自语言、拓扑与引擎（装配项目实验计划的“探针组件”）。改契约时两个探针一起改，`conformance/` 的测试两边都跑。

## 用户面（HTTP，主端口）

`PROBE_GUARD` 为假（缺省）时这些路由是公开的；为真时每条路由需要用户凭据与表中的操作（授权视图来自授权组件）。

| 方法与路径 | 请求 | 回答 | 操作 |
| --- | --- | --- | --- |
| `POST /api/probe/echo` | `{"payload": "<字符串>"}` | 原样回送 | `test.probe.echo` |
| `POST /api/probe/work` | `{"ms": 0..1000}` | 消耗这么多毫秒的 CPU 后 `{"ms": n}` | `test.probe.work` |
| `POST /api/probe/write` | `{"bytes": 0..524288, "relay": 布尔}` | 一个事务内写一行并发一个事件，`201 {"id"}`；载荷超过 64 KiB 走对象存储的凭证检查（大事件） | `test.probe.write` |
| `POST /api/probe/call` | `{"op": "echo"\|"work", "surface": "grpc"\|"http", "bytes", "ms"}` | 调用 `PROBE_NEXT` 的同一操作，`{"ok": true}`；没有下一个探针答 409 `NO_NEXT_PROBE` | `test.probe.call` |
| `GET /api/probe/seen` | — | `{"count", "big", "relayed"}`：收到的对方事件数、其中超过 64 KiB 的数、已转发成调用的数 | `test.probe.read` |

## 系统面（gRPC，`grpc` 端口，mTLS）

| 方法 | 请求与回答 | 操作 |
| --- | --- | --- |
| `/test.probe.v1.Probe/Echo` | `google.protobuf.BytesValue`，原样回送 | `test.probe.echo` |
| `/test.probe.v1.Probe/Work` | `google.protobuf.UInt32Value`（毫秒，至多 1000） | `test.probe.work` |

允许的调用组件：`test/probe-go`、`test/probe-py`。

## 事件

每个探针向自己的流发 `test.probe.written.v1`（`test.probe_go`、`test.probe_py`，各 4 个分区），数据 `{"id", "relay", "blob"}`。`PROBE_PEER_STREAM` 指向对方的流时消费它：记一行；`relay` 为真时在事务之外调用 `PROBE_NEXT` 的 `Echo`（1 KiB）。Go 探针用 SDK 的后台工作项，Python 探针用自己的转发队列（Python SDK 子集没有后台工作项；认领规则相同：`FOR UPDATE SKIP LOCKED`、租约到期再认领、每 0.5 秒轮询、并发 4）。

## 资源与操作

资源 `test.probe`（无维度），查看操作 `test.probe.read`，动作 `test.probe.echo`、`work`、`write`、`call`，风险均为低。
