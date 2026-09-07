# be-acceptance 不是 brickKit 组件，但仍按总纲 §I 的 9 个门禁目标写。
.DEFAULT_GOAL := help
.PHONY: help check-version test image migrate-idempotent dag-check contract-check \
        import-scan smoke module-check gates tier0 all

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
# down && up，routine 的 `make test`/`make all` 不该顺手把这些跑了——
# 单独用 `make tier0` 触发。
test:  ## 跑除 closedloop/ 外的全部单测（-race）
	go test $$(go list ./... | grep -v '/closedloop$$') -race

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

##@ 汇总
all: check-version test image migrate-idempotent dag-check contract-check import-scan smoke module-check  ## 跑完整 9 项（不含 gates/tier0，含上面几条 N/A 直接过）
