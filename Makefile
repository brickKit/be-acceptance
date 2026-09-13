# be-acceptance 不是 brickKit 组件，但仍按总纲 §I 的 9 个门禁目标写。
.DEFAULT_GOAL := help
.PHONY: help check-version test image migrate-idempotent dag-check contract-check \
        import-scan smoke module-check gates tier0 tier1 tier2 all

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
# down && up；platform/ 的 20 条平台断言要真的起临时容器、跑 brickkit
# CLI，其中一条真等 45+ 秒；tier2/ 需要真实可达的 TEST_PG_DSN（不像
# closedloop/platform 那么破坏性，但同样是"需要真实外部前提"的一类）——
# routine 的 `make test`/`make all` 都不该顺手把这三块跑了，各自用
# `make tier0`/`make tier1`/`make tier2` 单独触发。
test:  ## 跑除 closedloop/、platform/、tier2/ 外的全部单测（-race）
	go test $$(go list ./... | grep -vE '/(closedloop|platform|tier2)$$') -race

dag-check:  ## 包依赖图无环（Go 编译器本身就不允许循环 import，这条恒过）
	@go list ./... >/dev/null && echo "✓ 包依赖图无环（Go 编译器本身就不允许循环 import）"

import-scan:  ## 铁律六：be-acceptance 不许依赖任何组件仓库（be-sdk-go 白名单例外）
	@bad="$$(go list -deps ./... 2>/dev/null | grep '^github.com/brickKit/' | grep -vE '^github.com/brickKit/be-acceptance($$|/)' | grep -vE '^github.com/brickKit/be-sdk-go($$|/)')"; \
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

# ⚠️ 20 条平台断言（设计书 §9.6.2）。每条测试自己在 t.TempDir() 里现搭
# 隔离的 brickkit 工作区，不碰装配仓库真实的 brickkit.yaml；用例 10 的
# 后半段要真等 45+ 秒（真实冷启动计时，不是 mock），所以给了比默认长
# 的超时。红的条目（当前是用例 9 后半段，一个真实的 brickKit bug）
# 原样跑、原样红——不许为了让这条命令全绿而悄悄放宽断言或跳过它
# （platform/README.md 记录了红的原因，§9.6.2 的判据）。
tier1:  ## 20 条平台断言，未完成的组（B/C/F）随 Task 20 逐步补齐
	go test ./platform/... -run TestPlatform -v -count=1 -timeout 300s

# ⚠️ 阶段四 Task 11：合并态专属断言（tier0/tier1 判据的延伸，用例 24-25，
# 见 platform/README.md）。"共享连接池下 SET LOCAL 越权测试"（用例 25）
# 落在本仓库的 tier2/，只需要真实可达的 TEST_PG_DSN（先 make test-db-init）
# ——不需要 brickkit up，不碰 brickkit.yaml。"单个模块 panic 不拖垮外壳
# 其余模块"（用例 24）物理上做不到放进本仓库：它要真的调用 shells/go 的
# internal/shell.Run，那是另一个 Go module 自己的 internal 包，跨 module
# 天然不可见（Go 编译器层面的限制，不是铁律六 import-scan 门禁的问题）
# ——所以这条目标额外 cd 进 ../../shells/go 跑那条测试，两边都需要真实
# 可达的 TEST_PG_DSN/TEST_NATS_URL（同 shells/go 自己 real_modules_test.go
# 的既有前提，未设置就跳过，不是放宽断言）。
tier2:  ## 合并态专属断言：SET LOCAL 越权（本仓库）+ 单模块 panic 隔离（shells/go）
	go test ./tier2/... -run TestTier2 -v -count=1 -timeout 60s
	cd ../../shells/go && go test ./internal/shell/... -run 'TestRun_一个模块panic不影响其它真实模块继续服务' -v -count=1 -timeout 60s

##@ 汇总
all: check-version test image migrate-idempotent dag-check contract-check import-scan smoke module-check  ## 跑完整 9 项（不含 gates/tier0，含上面几条 N/A 直接过）
