# closedloop —— 档 2 业务闭环清单（设计书附录 E）

被测对象是**我们的业务**，验的是"一条跨组件业务链真的跑通"，不是平台本身（平台断言在 `../platform/`）。

⚠️ **`tier0_test.go` 也放在这个目录**（Task 17，`make tier0`）——它是档 0 的六项验收，规模比下面这张档 2 大表小得多（单组件闭环，不是跨组件业务链），但同属"真的把业务跑起来验，不是验平台断言"这一类，按计划原文的文件位置放在这里，不单独开一个 `tier0/` 目录。

## 现状（阶段一 Task 17）

档 0 六项验收（`tier0_test.go`）已实现并全绿，见根 `docs/dev/实测踩坑记录.md`（Task 16/17 相关条目）与 `../platform/README.md`。下面这张档 2 大表仍只是清单——**阶段三**（档 2，加 `crm/opportunity`、`infra-iam-casdoor`、`infra-workflow`、`infra-notification`、1 个 IM 通道、`infra-print`、`infra-bff-mobile`、`frontend-standard` 共 13 个组件后）逐步实现为可执行测试。

## 场景：CRM 赢单转 ERP 订单（附录 E 事件沙盘）

| # | 步骤 | 断言什么 | 状态 |
|---|---|---|---|
| 1 | 销售在 `crm-opportunity` 标记商机赢单 | 状态更新成功，`event_outbox` 里出现一条待发布记录 | ⏳ 待实现 |
| 2 | `crm-opportunity` 发布 `crm.opportunity.won.v1` | NATS 上能收到这条事件；`event_outbox` 该行标记已发布 | ⏳ 待实现 |
| 3 | `erp-sales` 消费该事件 | `event_inbox` 记录幂等键；重复投递同一事件不重复处理 | ⏳ 待实现 |
| 4 | `erp-sales` 同步调用 `mdm-customer`/`mdm-product` 的 `batchGet` | 拿到的是最新客户/产品数据，不是事件里携带的旧快照 | ⏳ 待实现 |
| 5 | `erp-sales` 创建销售订单，写入 Outbox，发布 `sales.order.created.v1` | 订单落库；事件因果链 `causation_id` 指向步骤 2 的事件 | ⏳ 待实现 |
| 6 | `erp-inventory` 消费该事件，锁定库存 | 库存扣减/预留正确；重放同一事件不重复扣减（幂等） | ⏳ 待实现 |
| 7 | `erp-finance` 消费该事件，生成应收账款凭证 | 凭证金额、客户与订单一致 | ⏳ 待实现 |
| 8 | 走一遍审批（`infra-workflow`） | 待办生成、审批通过后状态流转正确 | ⏳ 待实现 |
| 9 | 钉钉通知（`integration-im-dingtalk` + `infra-notification`） | 审批结果确实推送到钉钉 | ⏳ 待实现 |
| 10 | 打印送货单 PDF（`infra-print`，Python） | 生成的 PDF 内容与订单数据一致 | ⏳ 待实现 |
| 11 | Saga 补偿：让下游某一步故意失败 | 已执行的上游步骤被正确补偿（非"补偿的补偿"死循环，决策 44） | ⏳ 待实现 |
| 12 | 同步调用超时 | 上游不直接判失败，先查询下游状态再决定（决策 37，禁"薛定谔的超时"） | ⏳ 待实现 |
| 13 | DLQ 进得去出得来 | 重试耗尽后消息进 DLQ；`hop_count > 5` 被丢弃；DLQ 消息能被重新投递（不含管理界面/积压告警——那是档 4a `infra-dlq-monitor` 的事） | ⏳ 待实现 |
