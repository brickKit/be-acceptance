# be-acceptance 不是 brickKit 组件，但仍按总纲 §I 的 9 个门禁目标写。
.PHONY: check-version test image migrate-idempotent dag-check contract-check \
        import-scan smoke module-check gates all

check-version:
	@echo "N/A：非组件仓库，没有 component.yaml"

test:
	@pkgs="$$(go list ./... 2>/dev/null)"; \
	if [ -z "$$pkgs" ]; then \
		echo "⏳ 还没有 .go 文件（Task 9 起才有），暂不算失败"; \
		exit 0; \
	fi; \
	go test ./... -race

image:
	@echo "N/A：没有 main 包，不产出可执行文件"

migrate-idempotent:
	@echo "N/A：非组件仓库，没有迁移"

dag-check:
	@go list ./... >/dev/null && echo "✓ 包依赖图无环（Go 编译器本身就不允许循环 import）"

contract-check:
	@echo "N/A：非组件仓库，没有 contracts/"

import-scan:
	@bad="$$(go list -deps ./... 2>/dev/null | grep '^github.com/brickKit/' | grep -vE '^github.com/brickKit/be-acceptance($$|/)' | grep -vE '^github.com/brickKit/be-sdk-go($$|/)')"; \
	if [ -n "$$bad" ]; then \
		echo "✗ be-acceptance 不许依赖任何组件仓库：$$bad"; exit 1; \
	fi; \
	echo "✓ 零组件依赖"

smoke:
	@echo "N/A：非组件仓库，没有 brickkit up 的对象"

module-check:
	@echo "N/A：非组件仓库，没有 module.New 契约"

# gates：本仓库自己的活——铁律六 import 扫描 + 拆回门禁（阶段四加）+
# 平台验收 20 条 + 业务闭环。Task 9 起逐个实现，现在只是占位。
gates:
	@echo "⏳ 尚未实现（Task 9）"

all: check-version test image migrate-idempotent dag-check contract-check import-scan smoke module-check
