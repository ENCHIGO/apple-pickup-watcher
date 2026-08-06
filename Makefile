# apple-pickup-watcher 构建入口
#
# 上游把打包逻辑单独写在 build.sh 里，且只覆盖 macOS 打包一件事，日常开发的
# 构建、测试、检查全靠手敲。这里把常用动作统一收进 make，CI 与本地跑的是同
# 一套命令，避免出现「本地能过、CI 挂掉」这类无谓的返工。

BINARY   := apple-pickup-watcher
APP_NAME := Apple Pickup Watcher
APP_ID   := io.github.enchigo.apple-pickup-watcher
MAIN     := ./cmd/$(BINARY)
DIST     := dist

# 图标不在仓库里时 package-mac 会提前给出明确提示，而不是让 fyne 抛一句
# 难以理解的错。换图标直接 make package-mac ICON=path/to/icon.png。
ICON ?= assets/icon.png

# -ldflags "-s -w" 去掉符号表与调试信息，体积能小三成左右；
# -trimpath 抹掉编译机的绝对路径，避免把本地目录结构带进发布产物。
GOFLAGS := -trimpath
LDFLAGS := -s -w

# migrated_fynedo 向 Fyne 声明「本应用的界面更新已全部经由 fyne.Do 调度」。
# 不加这个标签，Fyne 每次启动都会打印三行「本应用尚未迁移到 fyne.Do 线程模型」
# 的警告。对本项目而言那是一句不实陈述 —— internal/ui 的所有控件访问都走 onMain，
# 并有 internal/ui/ui_test.go 里的用例钉住 —— 把它发给用户只会造成误会。
#
# 只加给产出二进制的目标。test 与 vet 刻意不加：这个标签同时会关掉 Fyne 的
# 跨线程访问检查，而那正是开发期和 CI 里最该开着的东西。
BUILD_TAGS := migrated_fynedo

.DEFAULT_GOAL := build
.PHONY: help build test vet fmt fmt-check run clean package-mac

help: ## 列出所有可用目标
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

build: ## 编译到 dist/
	@mkdir -p $(DIST)
	go build $(GOFLAGS) -tags "$(BUILD_TAGS)" -ldflags "$(LDFLAGS)" -o $(DIST)/$(BINARY) $(MAIN)

# -race 不是可选项：本项目的核心目标之一就是修掉上游那种跨线程无锁读写共享
# map 导致的运行时崩溃，不开竞态检测的测试等于没测到点子上。
test: ## 带竞态检测跑全部测试
	go test -race ./...

vet: ## 静态检查
	go vet ./...

fmt: ## 按 gofmt 规范重排全部源码
	gofmt -w .

fmt-check: ## 只检查格式不修改，CI 用
	@unformatted=$$(gofmt -l .); \
	if [ -n "$$unformatted" ]; then \
		echo "以下文件未通过 gofmt，请先执行 make fmt："; \
		echo "$$unformatted"; \
		exit 1; \
	fi

run: ## 直接运行
	go run -tags "$(BUILD_TAGS)" $(MAIN)

# 只清测试缓存，不动编译缓存：把 build cache 也删掉会让下一次构建从零开始
# 编译整个 Fyne 依赖树，代价远大于收益。
clean: ## 清理构建产物
	rm -rf $(DIST) fyne-cross "$(APP_NAME).app" "$(MAIN)/$(APP_NAME).app"
	go clean -testcache

# fyne package 打包的是「当前目录下的包」，所以要先进到 main 包目录再执行；
# 图标必须用绝对路径传入，否则会被当作相对 main 包目录的路径而找不到。
#
# 结尾的 xattr -cr 与 ad-hoc 签名不能省：未签名的 .app 一旦带上下载隔离属性，
# Gatekeeper 会直接拒绝启动并报「已损坏」，用户根本看不出真实原因。上游
# issue #111「M4 Pro 上跑不起来」很可能就是这个。
package-mac: ## 打包成 macOS .app 并做 ad-hoc 签名
	@if [ "$$(uname -s)" != "Darwin" ]; then \
		echo "package-mac 只能在 macOS 上执行：Fyne 依赖 CGO，.app 必须原生构建"; \
		exit 1; \
	fi
	@command -v fyne >/dev/null 2>&1 || { \
		echo "缺少 fyne 命令，请先执行："; \
		echo "  go install fyne.io/tools/cmd/fyne@latest"; \
		exit 1; \
	}
	@test -f "$(ICON)" || { \
		echo "找不到图标文件 $(ICON)，请放置图标或指定 ICON=<路径>"; \
		exit 1; \
	}
	@mkdir -p $(DIST)
	cd $(MAIN) && fyne package -name "$(APP_NAME)" -appID $(APP_ID) -icon "$(CURDIR)/$(ICON)" -tags "$(BUILD_TAGS)"
	rm -rf "$(DIST)/$(APP_NAME).app"
	mv "$(MAIN)/$(APP_NAME).app" "$(DIST)/"
	xattr -cr "$(DIST)/$(APP_NAME).app"
	codesign --force --deep --sign - "$(DIST)/$(APP_NAME).app"
	@echo "已生成 $(DIST)/$(APP_NAME).app"
