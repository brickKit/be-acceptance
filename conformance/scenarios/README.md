# L3 场景

跨组件的业务链（测试规范的 L3）：对一个已经起来、灌好种子的格，经各组件的用户面走一遍业务，检查组件之间发生的事——预占与释放、交货出库、认领与作废、正式化后订单完成、部门事件改写销售的范围、打印。每个测试自己建单据（新的幂等键），只依赖种子数据里的固定标识（装配项目的 `docs/zh/03-seed-data.md`），可以反复跑。

| 文件 | 业务链 | 涉及的组件 |
| --- | --- | --- |
| `test_trading_chain.py` | 订单从确认到打印的账单；信用或库存拒绝时释放另一方的预占；取消释放两边；草拟认领与作废；部门移动 | `erp/sales`、`erp/inventory`、`erp/credit`、`erp/billing`、`infra/print`、`mdm/org` |

测试名写出对方组件（测试规范的命名规则），用 `-k` 按名字只跑两个组件之间的。

## 运行

格起来并灌好种子之后（`cells create`、`prepare`、`brickkit build`、`brickkit up`、`cells secure`，装配项目根目录 `make seed-data`）：

```sh
BE_ACC_CELL=<格的目录，绝对路径> BE_ACC_IAM_TEST_PASSWORD=<测试账号的密码> make -C .. scenarios   # 在 conformance/ 里：uv run pytest -q scenarios
```

单跑的格与外壳的格用同一套：地址都是格的主机端口（`10000 + 端口`）。以 `test.bob`（种子与负载用户）建单据，以销售账号检查范围。

## 不在这里的

- 进程崩溃后的恢复（协议 04 的 RS06：最后一次提交与本地确认之间崩溃）要在一步的中间停掉进程，属于故障注入（套件 C），不在这里。
