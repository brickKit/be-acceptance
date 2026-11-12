# L3 场景

跨组件的业务链（测试规范的 L3）：对一个已经起来、灌好种子的格，经各组件的用户面走一遍业务，检查组件之间发生的事——预占与释放、交货出库、认领与作废、正式化后订单完成、部门事件改写销售的范围、打印。每个测试自己建单据或任职（新的幂等键），只依赖种子数据里的固定标识（装配项目的 `docs/zh/03-seed-data.md`），可以反复跑。

| 文件 | 业务链 | 涉及的组件 |
| --- | --- | --- |
| `test_trading_chain.py` | 订单从确认到打印的账单；信用或库存拒绝时释放另一方的预占；取消释放两边；草拟认领与作废；部门移动 | `erp/sales`、`erp/inventory`、`erp/credit`、`erp/billing`、`infra/print`、`mdm/org` |
| `test_expectations.py` | 协议 04、05 里能从用户面驱动的独立预期，测试名带编号（下表） | `erp/sales`、`erp/inventory`、`erp/credit`、`erp/billing` |
| `test_scope.py` | 谁看得到、谁能写：没有授予的账号什么都看不到；销售只看自己法人与部门的单、范围外读不到也写不了；只有出账角色能开具 | 同上，`mdm/org` |
| `test_employee.py` | 绑定任职的授予：任职在职时给、结束后立即不给并记为结束，新任职不自动有；处置一个账号时同一个人（关联同一员工）的另一个账号也被挡住，解除后恢复。格里没有 `mdm/employee` 时跳过 | `mdm/employee`、`infra/authz`、`mdm/org` |
| `test_purchase.py` | 采购到入库再退回：经运行中的库存收货，库存增加、流水带收货行参考、订单的已收等于流水、收足即完成；从那条流水退货，库存减少；超过法人的超收比例整笔拒绝、库存不动；提交人不能批准；采购员只看采购部的订单。格里没有 `erp/purchase` 时跳过 | `erp/purchase`、`erp/inventory`、`infra/authz`、`mdm/org` |
| `test_finance.py` | 从订单到凭证：开具的账单（出账的事件）形成应收与已过账的凭证（应收借、收入与销项税贷，凭证号连续）；采购收货后登记供应商票据，多于已收进入差异、登记人不能批准，改成已收数量后匹配、过账成应付与凭证；会计制单不能审批、主管审批取号，会计看不到别的法人的凭证。格里没有 `erp/finance` 时跳过 | `erp/finance`、`erp/billing`、`erp/sales`、`erp/purchase`、`erp/inventory`、`infra/authz` |
| `test_payment.py` | 收付款：为开具的账单登记收款，财务按资金事实核销这位客户的应收（最早到期的先分，已核销合计增加到账的金额）；采购收货、供应商票据过账后，付款申请提交时认领应付（认领中、可用为零），申请人不能批准，主管批准、出纳放行与登记执行后财务把应付核销到零。格里没有 `erp/payment` 或 `erp/finance` 时跳过 | `erp/payment`、`erp/finance`、`erp/billing`、`erp/sales`、`erp/purchase`、`erp/inventory` |
| `test_costing.py` | 存货成本：采购收货的成本层按收货计价依据（订单价）计价、发存货借暂估贷的成本事实；更高价的供应商票据经主管批准差异、过账后替换依据，在库单位的差额出向上调整，两条成本事实都在财务过成凭证；销售交货后计价范围的数量跟上法人的库存（CN01 移动平均）；ES01 先进先出：交货取走全部旧库存与新收货的一个单位，采购退货问成本组件，两个被拒（`RETURN_EXCEEDS_COST_LAYER`）、在库的一个退回并按层价出退货事实。格里没有 `erp/costing` 时跳过 | `erp/costing`、`erp/purchase`、`erp/finance`、`erp/sales`、`erp/inventory`、`erp/credit` |
| `test_opportunity.py` | 商机到订单（A71）：商机请求报价，销售生成一张没有明细、带商机参考与请求身份的报价草稿（商机上的请求变成已生成）；没有明细不能发出，补上明细后发出、接受成订单（订单带商机参考），确认订单后商机按订单的不含税合计自动赢单（格设 `OPPORTUNITY_AUTO_WIN=order_confirmed`，操作者 `system`）；`test.sales.cn` 只看到、只算自己的商机，别人的答 404。格里没有 `crm/opportunity` 时跳过 | `crm/opportunity`、`erp/sales`、`erp/inventory`、`erp/credit` |

测试名写出对方组件（测试规范的命名规则），用 `-k` 按名字只跑两个组件之间的。

## 运行

格起来并灌好种子之后（`cells create`、`prepare`、`brickkit build`、`brickkit up`、`cells secure`，装配项目根目录 `make seed-data`）：

```sh
BE_ACC_CELL=<格的目录，绝对路径> BE_ACC_IAM_TEST_PASSWORD=<测试账号的密码> make -C .. scenarios   # 在 conformance/ 里：uv run pytest -q scenarios
```

单跑的格与外壳的格用同一套：地址都是格的主机端口（`10000 + 端口`）。以 `test.bob`（种子与负载用户）建单据，以销售账号检查范围。

## 独立预期在哪里测

第 8 步定（`dev/rebuild/step8-plan.md` 的 T8.4，在装配项目里）：能从用户面驱动的在这里，每个格都跑；要丢一次应答、等到期或卡时序的在组件的 L2（替身能做到）；要停进程或停消息系统的在套件 C。

| 编号 | 在哪里 |
| --- | --- |
| RS01、RS08、RS16、RS19、BL01、BL02 | `test_expectations.py` |
| RS02 | `test_trading_chain.py` 的信用拒绝释放库存（`test_expectations.py` 里留了一个指过去的名字） |
| RS03、RS04、RS07、RS09、RS17 | 组件的 L2：`erp/sales`、`erp/inventory`、`erp/credit` 的集成测试（替身丢应答、迟到、慢、并发修改、同身份不同数量） |
| BL05、BL21、BL24、BL25、BL26 | 组件的 L2：`erp/billing`、`erp/sales` 的集成测试（认领到期、应答丢失、恢复工作项与编号竞争、迟到的确认） |
| RS05、RS06、RS18、BL04 | 套件 C（协调者崩溃、本地确认前崩溃、消息系统停、正式化前崩溃） |

## 不在这里的

- 进程崩溃后的恢复（协议 04 的 RS06：最后一次提交与本地确认之间崩溃）要在一步的中间停掉进程，属于故障注入（套件 C），不在这里。
