# L3 场景

跨组件的业务链（测试规范的 L3）：对一个已经起来、灌好种子的格，经各组件的用户面走一遍业务，检查组件之间发生的事——预占与释放、交货出库、认领与作废、正式化后订单完成、部门事件改写销售的范围、打印。每个测试自己建单据（新的幂等键），只依赖种子数据里的固定标识（装配项目的 `docs/zh/03-seed-data.md`），可以反复跑。

| 文件 | 业务链 | 涉及的组件 |
| --- | --- | --- |
| `test_trading_chain.py` | 订单从确认到打印的账单；信用或库存拒绝时释放另一方的预占；取消释放两边；草拟认领与作废；部门移动 | `erp/sales`、`erp/inventory`、`erp/credit`、`erp/billing`、`infra/print`、`mdm/org` |
| `test_expectations.py` | 协议 04、05 里能从用户面驱动的独立预期，测试名带编号（下表） | `erp/sales`、`erp/inventory`、`erp/credit`、`erp/billing` |
| `test_scope.py` | 谁看得到、谁能写：没有授予的账号什么都看不到；销售只看自己法人与部门的单、范围外读不到也写不了；只有出账角色能开具 | 同上，`mdm/org` |

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
