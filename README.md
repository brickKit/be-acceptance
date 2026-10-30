# be-acceptance

装配项目的验收工具：参考模型与文档检查。旧版本（0.x：门禁、一致性套件 compconf、闭环测试）在分支 `legacy/v0`，标签保留；本主分支按终局设计从零开始（D59）。

## 内容

| 目录 | 是什么 | 怎样运行 |
| --- | --- | --- |
| `models/` | 参考模型：用随机交错演算各协议的关键条款，正确版本通过、每个坏变体被发现。模型只证明协议在模型中的性质，不是 SDK、数据库或消息系统的运行结果 | `make models`，或 `python3 models/<编号>-<名字>.py` |
| `probes/` | 两种语言的通信探针 `test/probe-go`（`probes/go/`）与 `test/probe-py`（`probes/py/`），同一份契约（`probes/CONTRACT.md`）；各自是一个 BrickKit 组件，发布标签 `test-probe-<语言>/<版本>`，Go 另有模块标签 `probes/go/v<版本>`（模块路径带主版本：`github.com/brickKit/be-acceptance/probes/go/v3`） | 见各自的 `AGENTS.md` |
| `conformance/` | 从外部运行探针与组件：两种探针单跑、互相调用与互发事件；核心剖面（`profile/`，13 项检查，每项先过自检；报告按协议 18 判定）；实验的格（`cells/`：从 `cell.yaml` 建 BrickKit 项目、准备库与证书、对部署好的格跑剖面、测量与成批运行、出表）；资源采集（`measure/`：进程、容器、平台、连接数、积压） | `make probes`；`uv run python -m cells <create\|prepare\|profile\|run\|campaign\|tables> …`（要真实的 PostgreSQL、NATS 与对象存储，环境变量见 `conformance/harness.py`） |
| `conformance/scenarios/` | L3 跨组件业务链：对起来并灌好种子的格，经用户面走业务（交易链：确认、预占与释放、交货、出账、打印、部门事件），见其 `README.md` | `make scenarios`（`BE_ACC_CELL=<格>`） |
| `conformance/faults/` | 套件 C 的故障：对起来并灌好种子的格，从组件外部让进程崩溃、在一步的中间崩溃、停掉依赖，检查恢复后的业务结果（RS05、RS06、RS18、BL04），见其 `README.md`；整个格的不变量 `python -m cells invariants` | 一次一个：`uv run pytest -q faults -k <名字>`（`BE_ACC_CELL`、`BE_ACC_PG_ADMIN`） |
| `load/` | 负载驱动（Go）：开环固定速率、延迟从该发出的时刻算、调用方测的一跳、加压到 p99、错误或饱和；探针的单个操作，或业务链（`-flow confirm\|deliver\|bill\|print`：每次迭代下单并依次确认、交货、草拟、开具、打印，按步骤汇总）；`-series` 另写每一秒的发出、成功、失败原因与延迟（套件 C 看故障从哪一秒开始、到哪一秒恢复）；收到 SIGINT 或 SIGTERM 提前结束，照样写出结果 | 探针由 `cells run` 构建与调用；业务链先 `python -m cells world <格> <文件>`，再 `go run . -flow bill -world <文件> -credentials <格>/.secrets/load-user.json`；`go test .` |
| `doc-checks/` | 文档检查：预期编号（33）、必需依赖无环与组合闭包（34）、本地链接与锚点（含现行文档不链接 `archive/`、`dev/`）；另有安全记录 JSON Schema 的生成与核对（`security-schemas.py`：从安全 09a 与组件协议第 08 章的字段表生成，写进 `be-protocol` 的 `schemas/security/`） | `make doc-checks PROJECT=<装配项目根目录>`（检出在装配项目的 `tools/be-acceptance` 时默认就是项目根目录） |

模型与检查的编号沿用设计文档中的写法（“检查 33”“模型 17”），对照表在装配项目的协议 21（`docs/zh/06-protocols/21-verification-and-migration.md`）。

验收矩阵的可执行形式与门禁，随重建的 SDK 与组件加入（协议 18、21）。

## 要求

参考模型与文档检查：Python 3.11 及以上，只用标准库（模型 08 用标准库的 `sqlite3`）。探针与 `conformance/`：Go 1.27、Python 3.15 与 `uv`，依赖锁在各自的 `uv.lock`、`go.sum`。模型 35 约需 13 分钟。
