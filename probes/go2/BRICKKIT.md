# test/probe-go2

## 组件定位

`test/probe-go` 的变体：同一份代码（be-acceptance 仓库的 `probes/go/`）以另一个组件 ID、端口、schema 与事件流 `test.probe_go2` 运行，用来让一个外壳承载多个探针、让两个探针在同一进程里互相调用。行为、契约与配置都与 `test/probe-go` 相同（be-acceptance 仓库的 `probes/CONTRACT.md`）。

## 部署前准备

同 `test/probe-go`：自己的 schema 与角色（注册表的 `test_probe_go2`）、消息系统、对象存储的桶与前缀、部署的服务证书。

## 依赖说明

不声明组件依赖：下一个探针由部署指定（`PROBE_NEXT` 加上它的地址变量）。

## 配置指南

键与 `test/probe-go` 相同（`component.yaml` 的 `configSchema`）。必填：`PG_HOST`、`PG_DATABASE`、`PG_USER`、`PG_PASSWORD`、`PG_SCHEMA`（自己的数据库目标、运行角色与 schema，密码是密钥）；`BUS_URL`（消息系统，密钥）；`IMPLEMENTATION_INSTANCE`（生产者实例的身份）。

## 契约索引

同 `test/probe-go`；资源类型与操作键的前缀是 `test.probe_go2`，事件流是 `test.probe_go2`。

## 外壳声明

不是外壳。可以被Go外壳承载。
