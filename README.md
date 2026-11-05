# be-acceptance

装配项目的验收工具：参考模型与文档检查。旧版本（0.x：门禁、一致性套件 compconf、闭环测试）在分支 `legacy/v0`，标签保留；本主分支按终局设计从零开始（D59）。

## 内容

| 目录 | 是什么 | 怎样运行 |
| --- | --- | --- |
| `models/` | 参考模型：用随机交错演算各协议的关键条款，正确版本通过、每个坏变体被发现。模型只证明协议在模型中的性质，不是 SDK、数据库或消息系统的运行结果 | `make models`，或 `python3 models/<编号>-<名字>.py` |
| `probes/` | 两种语言的通信探针 `test/probe-go`（`probes/go/`）与 `test/probe-py`（`probes/py/`），同一份契约（`probes/CONTRACT.md`）；各自是一个 BrickKit 组件，发布标签 `test-probe-<语言>/<版本>`，Go 另有模块标签 `probes/go/v<版本>`（模块路径带主版本：`github.com/brickKit/be-acceptance/probes/go/v3`） | 见各自的 `AGENTS.md` |
| `conformance/` | 从外部运行探针与组件：两种探针单跑、互相调用与互发事件；核心剖面（`profile/checks.py`，13 项检查）与事件剖面（`profile/events.py`，Go 探针在两种运输上：CK-EV-01…03、05…08，CK-BUS-09、10）、后台工作剖面（`profile/work.py`，Go 探针被停止、杀掉、推进恢复代次时的工作项：CK-WK-01…04、06…10）与水位来源剖面（`profile/watermark.py`，套件以声明的补取者与陌生组件补取 Go 探针的水位流，并像没推进代次的恢复那样改它的发件表：CK-WM-01…03，SK17）、身份剖面（`profile/identity.py`，套件当身份提供方与授权成员，开着守卫驱动两种探针：公钥轮换、日志里没有凭据、系统面拒绝用户凭据与不合格的证书、令牌陈旧、下游的 401：CK-ID-01…06）与授权剖面（`profile/authz.py`，套件当授权成员，开着守卫驱动两种探针的条目：未声明的操作启动失败、随机数不符不续期、授权成员停止与恢复代次回退、事务中视图过期回滚、四种维度上别人的看不见、越界取值被拒、列表与单对象一致与掩码：CK-AZ-01…05）、对外命令剖面（`tests/test_external_profile.py`，Go 探针的付款对假外部系统 `external/fake.py`：结果不明先查、按去重与否决定重发、不重复付款、不可撤回的没有发出记录就不发；Python 探针随第 6 项），每项先过自检；报告按协议 18 判定；实验的格（`cells/`：从 `cell.yaml` 建 BrickKit 项目、准备库与证书、对部署好的格跑剖面、测量与成批运行、出表）；资源采集（`measure/`：进程、容器、平台、连接数、积压） | `make probes`；`uv run python -m cells <create\|prepare\|profile\|run\|campaign\|tables> …`（要真实的 PostgreSQL、NATS 与对象存储，环境变量见 `conformance/harness.py`） |
| `conformance/scenarios/` | L3 跨组件业务链：对起来并灌好种子的格，经用户面走业务（交易链：确认、预占与释放、交货、出账、打印、部门事件），见其 `README.md` | `make scenarios`（`BE_ACC_CELL=<格>`） |
| `conformance/faults/` | 套件 C 的故障：对起来并灌好种子的格，从组件外部让进程崩溃、在一步的中间崩溃、停掉依赖，检查恢复后的业务结果（RS05、RS06、RS18、BL04），见其 `README.md`；整个格的不变量 `python -m cells invariants` | 一次一个：`uv run pytest -q faults -k <名字>`（`BE_ACC_CELL`、`BE_ACC_PG_ADMIN`） |
| `load/` | 负载驱动（Go）：开环固定速率、延迟从该发出的时刻算、调用方测的一跳、加压到 p99、错误或饱和；探针的单个操作，或业务链（`-flow confirm\|deliver\|bill\|print`：每次迭代下单并依次确认、交货、草拟、开具、打印，按步骤汇总）；`-series` 另写每一秒的发出、成功、失败原因与延迟（套件 C 看故障从哪一秒开始、到哪一秒恢复）；收到 SIGINT 或 SIGTERM 提前结束，照样写出结果 | 探针由 `cells run` 构建与调用；业务链先 `python -m cells world <格> <文件>`，再 `go run . -flow bill -world <文件> -credentials <格>/.secrets/load-user.json`；`go test .` |
| `doc-checks/` | 文档检查：预期编号（33）、必需依赖无环与组合闭包（34）、本地链接与锚点（含现行文档不链接 `archive/`、`dev/`）；另有安全记录 JSON Schema 的生成与核对（`security-schemas.py`：从安全 09a 与组件协议第 08 章的字段表生成，写进 `be-protocol` 的 `schemas/security/`） | `make doc-checks PROJECT=<装配项目根目录>`（检出在装配项目的 `tools/be-acceptance` 时默认就是项目根目录） |

模型与检查的编号沿用设计文档中的写法（“检查 33”“模型 17”），对照表在装配项目的协议 21（`docs/zh/06-protocols/21-verification-and-migration.md`）。

验收矩阵的可执行形式与门禁，随重建的 SDK 与组件加入（协议 18、21）。

## 要求

参考模型与文档检查：Python 3.11 及以上，只用标准库（模型 08 用标准库的 `sqlite3`）。探针与 `conformance/`：Go 1.27、Python 3.15 与 `uv`，依赖锁在各自的 `uv.lock`、`go.sum`。模型 35 约需 13 分钟。

## 发布

工具本身打 `v<版本>` 的带注释标签；探针各自用 `brickkit release --notes-file` 发布（`probes/py`、`probes/py2` 与 Go 的变体）。**打 `v` 标签之前跑 `make check`**：不需要基础资源的单元测试（格工具、测量、剖面检查的自检）与探针变体是否最新。要动探针或格的行为时，再跑 `make probes`（需要基础资源）。1.0.18 只在真机上验证、没跑单元测试就发了，弄坏了 `test_units.py`。
