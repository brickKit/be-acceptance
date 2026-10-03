# be-acceptance 不是 brickKit 组件，但仍按总纲 §I 的 9 个门禁目标写。
.DEFAULT_GOAL := help
.PHONY: compconf-unit compconf-infra compconf-selftest help check-version test image migrate-idempotent dag-check contract-check \
        import-scan smoke module-check gates tier0 tier1 tier2 tier2-shell all

help:  ## 列出所有目标
	@awk 'BEGIN{FS=":.*##"; printf "\n用法: make <目标>\n\n"} \
	     /^[a-zA-Z0-9_-]+:.*##/ {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2} \
	     /^##@/ {printf "\n\033[1m%s\033[0m\n", substr($$0,5)}' $(MAKEFILE_LIST)
	@echo ""

##@ 9 个门禁里对本仓库没意义的（非组件仓库，没有对应的东西）
check-version:  ## N/A：没有 component.yaml
	@echo "N/A：非组件仓库，没有 component.yaml"

image:  ## N/A：本仓库是本地/CI 用的验收 CLI，不作为 brickKit 服务部署
	@echo "N/A：本仓库是本地/CI 用的验收 CLI，不作为 brickKit 服务部署，不需要镜像"

migrate-idempotent:  ## N/A：没有迁移
	@echo "N/A：非组件仓库，没有迁移"

contract-check:  ## N/A：没有 contracts/
	@echo "N/A：非组件仓库，没有 contracts/"

smoke:  ## N/A：没有 brickkit up 的对象
	@echo "N/A：非组件仓库，没有 brickkit up 的对象"

module-check:  ## N/A：没有 module.New 契约
	@echo "N/A：非组件仓库，没有 module.New 契约"

##@ 对本仓库真正有意义的
# ⚠️ closedloop/ 的档 0/档 2 测试要真的 docker stop postgres、brickkit
# down && up；tier2/ 需要真实可达的 TEST_PG_DSN（不像 closedloop/ 那么
# 破坏性，但同样是"需要真实外部前提"的一类）——routine 的 `make test`/
# `make all` 都不该顺手把这两块跑了，各自用 `make tier0`/`make tier2`
# 单独触发。
test:  ## 跑除 closedloop/、tier2/ 外的全部单测（-race）
	go test $$(go list ./... | grep -vE '/(closedloop|tier2)$$') -race

dag-check:  ## 包依赖图无环（Go 编译器本身就不允许循环 import，这条恒过）
	@go list ./... >/dev/null && echo "✓ 包依赖图无环（Go 编译器本身就不允许循环 import）"

import-scan:  ## 铁律六：be-acceptance 不许依赖任何组件仓库（be-sdk-go 与纯数据的 be-protocol、两个族契约模块白名单例外）
	@bad="$$(go list -deps ./... 2>/dev/null | grep '^github.com/brickKit/' | grep -vE '^github.com/brickKit/be-acceptance($$|/)' | grep -vE '^github.com/brickKit/be-sdk-go($$|/)' | grep -vE '^github.com/brickKit/(be-protocol|contract-infra-authz/v2|contract-infra-iam)($$|/)')"; \
	if [ -n "$$bad" ]; then \
		echo "✗ be-acceptance 不许依赖任何组件仓库：$$bad"; exit 1; \
	fi; \
	echo "✓ 零组件依赖"

##@ 门禁 / 验收
# gates：本仓库自己的活——铁律六 import 扫描（已实现，Task 9）+ 拆回门禁
# （阶段四加）+ 平台验收 20 条 + 业务闭环（各自先决组件出现后逐条实现）。
# 规范入口是仓库根的 `make gates`（--root 指向装配根）；这里的目标假设
# 本仓库位于 <装配根>/tools/be-acceptance/，只在单独调试本仓库时使用。
gates:  ## 铁律六 import 扫描（对着装配根跑，单独调试本仓库时用）
	@go build -o build/be-acceptance ./cmd/be-acceptance
	@./build/be-acceptance gate import-scan --root ../..

# ⚠️ 会真的临时停掉 postgres、跑一次 brickkit down/up——先确认没有别人在用。
tier0:  ## 档 0 六项验收，每加一个组件都要重跑（§9.6.1 档 4）
	go test ./closedloop/ -run 'Test档0' -v -count=1

# v0.4 的 platform/ 平台断言已在 be-acceptance v0.4.0 删除（它们按 brickKit
# v0.4 的行为写的），06f 按 v1 重写后再恢复这条目标。
tier1:  ## 已删除：平台断言等 06f 按 brickKit v1 重写
	@echo "tier1：platform/ 平台断言已随 v0.4.0 删除，06f 按 brickKit v1 重写"

# ⚠️ 阶段四 Task 11：合并态专属断言（tier0/tier1 判据的延伸，用例 24-25）。"共享连接池下 SET LOCAL 越权测试"（用例 25）
# 落在本仓库的 tier2/，只需要真实可达的 TEST_PG_DSN（先 make test-db-init）
# ——不需要 brickkit up，不碰 brickkit.yaml。"单个模块 panic 不拖垮外壳
# 其余模块"（用例 24）物理上做不到放进本仓库：它要真的调用 shells/go 的
# internal/shell.Run，那是另一个 Go module 自己的 internal 包，跨 module
# 天然不可见（Go 编译器层面的限制，不是铁律六 import-scan 门禁的问题）
# ——所以放进单独的 tier2-shell 目标，在外壳包所在的 Go module 里跑，
# 两边都需要真实可达的 TEST_PG_DSN/TEST_NATS_URL（同外壳包自己
# real_modules_test.go 的既有前提，未设置就跳过，不是放宽断言）。
#
# ⚠️ v1 布局下外壳包（internal/shell）要迁到 be-sdk-go 的 shell/ 目录，
# 迁移落地之前这个目录不存在——SHELL_PKG_DIR 默认指向 be-sdk-go 的
# shell/，目录不在时 tier2-shell 明确提示并跳过，不去碰一条已经不存在
# 的 ../../shells/go 路径；迁完或想对着别处跑时 make tier2 SHELL_PKG_DIR=<dir>。
SHELL_PKG_DIR ?= ../be-sdk-go/shell

# ⚠️ 这两条断言防"空转变绿"（scripts/require-pass.sh）：go test 退出 0 不够，
# 还必须真有一条名字匹配的 `--- PASS:`——缺 TEST_PG_DSN 全部 SKIP、-run
# 没匹配上（"no tests to run"）、目录不存在，都是失败。唯一的跳过方式是
# 显式 SKIP_SHELL=1（只管外壳包那一步）。
tier2:  ## 合并态专属断言：SET LOCAL 越权（本仓库）+ 单模块 panic 隔离（外壳包，见 tier2-shell）
	@./scripts/require-pass.sh . 'schema|SET LOCAL|越权' 'tier2 SET LOCAL 越权断言' -- ./tier2/... -run TestTier2 -count=1 -timeout 60s
	@$(MAKE) --no-print-directory tier2-shell

tier2-shell:  ## 单模块 panic 隔离：在外壳包所在 module（SHELL_PKG_DIR）里跑；没跑到会失败，SKIP_SHELL=1 显式跳过
	@if [ "$(SKIP_SHELL)" = "1" ]; then \
	  echo "tier2-shell：按 SKIP_SHELL=1 的显式要求跳过"; \
	else \
	  ./scripts/require-pass.sh "$(SHELL_PKG_DIR)" '[Pp]anic|隔离' 'tier2-shell panic 隔离断言' -- ./... -count=1 -timeout 60s; \
	fi

##@ 汇总
all: check-version test image migrate-idempotent dag-check contract-check import-scan smoke module-check  ## 跑完整 9 项（不含 gates/tier0，含上面几条 N/A 直接过）

##@ 组件一致性套件 compconf（conformance/component）
compconf-unit:  ## compconf 的单测（不需要 docker）
	go test -race ./conformance/component/...

compconf-infra:  ## 一次性 PG16 / NATS / 容器操作的真机测试（需要 docker）
	go test -tags compconf_docker -count=1 -run TestEnv ./conformance/component/infra/

compconf-selftest:  ## 先见红再信绿：rawstub 全绿，每个坏变体恰好在自己的用例上判红（需要 docker，约 10–15 分钟）
	go test -tags compconf_docker -count=1 -timeout 60m -v -run 'TestSelftest' ./conformance/component/ 2>&1 | tail -120
