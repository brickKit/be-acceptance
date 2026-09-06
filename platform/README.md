# platform —— 档 1 平台验收清单（设计书 §9.6.2）

这批用例的被测对象**是 brickKit 本身，不是我们的业务**。每一条对应设计书某处断言，跑一次就知道那处断言还成不成立——**平台升级后重跑，就是回归测试**。

⚠️ **这不是占位符，是任务清单。** 发现平台真有问题时，先在这里记一条用例（哪怕是红的），再回 brickKit 仓库修——不许在业务侧绕过去。绕过去的那一条，会在 62 个组件时变成 61 处绕法（设计书 §9.6.2）。

## 现状（阶段一 Task 9）

只有清单，20 条用例**阶段二**（档 1，加 `mdm/product`/`erp/inventory`/`erp/finance`/`erp/sales` 共 5 个组件后）逐条实现为可执行测试。

## 20 条清单

| # | 验什么 | 怎么验 | 对应本书 | 状态 |
|---|---|---|---|---|
| 1 | 未知键当场报错 | 往 `component.yaml` 塞一个 `assembly_role: optional`，期望 `up` 失败 | §3.5 | ✅ 已验证（Task 13，`mdm/customer`）：`brickkit up --dry-run` 报 `MANIFEST_INVALID`，退出码 1，明确点出 `assembly_role：未知字段（第 75 行）` |
| 2 | `assembly.yaml` 靠 `artifacts` 随组件分发 | 声明 `type: metadata`，`up` 后检查 `.brickkit/artifacts/<服务名>/metadata/assembly.yaml` 在 | §3.5 | ⏳ 待实现 |
| 3 | 地址变量名由 ID 推导、不带版本号 | 断言 `MDM_CUSTOMER_ENDPOINT=http://mdm-customer-1-0-0:8080` | §2.1、§3.4 | ⏳ 待实现 |
| 4 | 额外端口地址是 `http://` 而不是 `grpc://` | 断言 `MDM_CUSTOMER_GRPC_ENDPOINT` 的值以 `http://` 开头，且组件的 gRPC 客户端剥掉 scheme 后能连上 | §2.1 | ⏳ 待实现 |
| 5 | 弱依赖缺失时不注入那个变量 | 把可选依赖从 `brickkit.yaml` 删掉，断言容器里 `env` 里根本没有那个键（不是空串） | §3.6 | ⏳ 待实现 |
| 6 | 保留变量会被跳过 | 故意起一个 `otelEndpoint` 配置项，断言 `up` 给警告且容器里拿不到值 | §2.7.3 | ⏳ 待实现 |
| 7 | config 数组会被渲染成 `[a b c]` | 断言 `ENABLED_COMPONENTS` 必须写成逗号分隔字符串才可用 | §6.1 | ⏳ 待实现 |
| 8 | 启停跟着上层走 | 顶层写 `enabled: false`，断言下层跟着不启动；再验"删条目"与"`enabled: false`"不等价 | §3.6、§5.10 | ⏳ 待实现 |
| 9 | Fork 遮蔽按 `sources` 顺序 | 同 id 同 version 两份源，断言靠前的赢；再验改了 `metadata.id` 之后 `*_ENDPOINT` 消失 | §3.4.1 | ⏳ 待实现 |
| 10 | 默认启动宽限是 60 秒 | 造一个冷启动 45 秒的组件，不写 `startPeriodSeconds`，断言它能起来 | §12.3.5 | ⏳ 待实现 |
| 11 | `local: true` 的三件事 | 断言不生成 service、依赖方有 `extra_hosts: <服务名>:host-gateway`、注入端口换成 `localPort` | §13.1 | ⏳ 待实现 |
| 12 | `local: true` 的额外端口不被改写 | 断言依赖方拿到的 gRPC 地址仍是 Manifest 里声明的端口；再验两个 local 组件撞同一额外端口时 `up` 报错 | §3.5.1.1 | ⏳ 待实现 |
| 13 | `local: true` 不生成迁移容器 | 断言 `up` 给警告，且库里表没建 | §13.3 铁律五 | ⏳ 待实现 |
| 14 | `local: true` + `k8s` 直接报错 | 断言生成阶段失败 | 决策 88 | ⏳ 待实现 |
| 15 | K8s 下 labels 落在 Deployment 与 Pod 的 annotations | `--dry-run` 出清单，两处都断言 | §7.5.1 | ⏳ 待实现 |
| 16 | K8s 下 hostname 必填且唯一 | 两个组件写同一个 hostname，断言硬报错（不是静默） | §6.3 | ⏳ 待实现 |
| 17 | `up` 只打印建库语句，不建库 | 断言输出里有 `CREATE DATABASE`，而库并没有被创建 | §2.7.2 | ⏳ 待实现 |
| 18 | `remove` 连 `.archived/` 一起删 | 先 `sync` 归档，再 `remove`，断言归档目录也没了 | §9.4.2 | ⏳ 待实现 |
| 19 | 精确版本 | 写 `^1.2`，断言报错 | 四条铁律之一 | ⏳ 待实现 |
| 20 | 本地源不受签名约束 | 开 `requireSignature: true`，断言本地源组件照装 | §9.4.1 | ⏳ 待实现 |
