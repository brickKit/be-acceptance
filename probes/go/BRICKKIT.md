# test/probe-go

## 组件定位

验收用的 Go 通信探针：回送、消耗 CPU、一个事务内写一行并发事件、收到对方事件后调用下一个探针，用来量每条通信路径的代价。没有业务逻辑，不进货架；与另一种语言的探针实现同一份契约（be-acceptance 仓库的 `probes/CONTRACT.md`）。同一份代码另以变体的组件 ID 发布（`test/probe-go2`、`-go3`、`-go4`），外壳编进全部变体。

## 部署前准备

- 数据库：自己的 schema 与运行角色、属主角色（装配项目注册表的 `test_probe_go`）。
- 消息系统（`BUS_URL`）；对象存储的桶与前缀（`OBJECT_STORE_*`，大事件的凭证检查）。
- 部署的服务证书（`SERVICE_TLS_*`）：系统面只接受 mTLS。
- `PROBE_GUARD` 为真时还要身份与授权（`IAM_*`、`AUTHZ_GRPC_URL`）。

## 依赖说明

不声明组件依赖：要调用的下一个探针由部署指定（`PROBE_NEXT` 加上它的地址变量），实验按格组合探针。

## 配置指南

全部键见 `component.yaml` 的 `configSchema`（由 SDK 的键清单生成）。必填：`PG_HOST`、`PG_DATABASE`、`PG_USER`、`PG_PASSWORD`、`PG_SCHEMA`（自己的数据库目标、运行角色与 schema，密码是密钥）；`BUS_URL`（消息系统，JetStream 的 `nats://` 或 PostgreSQL 队列的 `pgqueue://`，密钥）；`IMPLEMENTATION_INSTANCE`（生产者实例的身份，部署工具给每个实例一个 UUID）。探针自己的键：`PROBE_NEXT`（下一个探针的组件 ID）、`PROBE_PEER_STREAM`（要消费的对方的流）、`PROBE_GUARD`（用户面是否要凭据）。

## 契约索引

用户面 `/api/probe/echo`、`/work`、`/write`、`/call`、`/seen`；系统面 `/test.probe.v1.Probe/Echo`、`/Work`；事件 `test.probe.written.v1`，流 `test.probe_go`；资源类型与操作键的前缀 `test.probe_go`；`/call` 的回答带调用方测的这一跳耗时 `elapsed_us`。完整定义在 be-acceptance 仓库的 `probes/CONTRACT.md`。

## 外壳声明

不是外壳。可以被 Go 或 Python 的外壳承载（按语言）。
