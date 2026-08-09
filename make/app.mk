# Go アプリ開発系（起動 / テスト / ビルド / 静的解析 / モック生成）

.PHONY: run run/debug run/outbox-worker test test/v test/race test/cover test/cover/check build build/batch build/outbox-worker build/all lint lint/diff lint/fix tools/golangci-lint tools/mockgen mock/gen gen/check

# golangci-lint のバージョンは .golangci-version 単一箇所で管理する（ローカルと CI の等価性のため）。
# go.mod の tool ディレクティブは採用しない: golangci-lint の依存がアプリ本体の
# モジュールグラフに混ざり、golang.org/x/text 等の共有依存を巻き上げてしまうため。
GOLANGCI_VERSION := $(shell cat .golangci-version)
GOLANGCI_BIN     := $(CURDIR)/bin/golangci-lint

# mockgen のバージョンは go.mod の go.uber.org/mock から導出する（別ファイルに二重管理しない）。
# //go:generate は裸の `mockgen` を呼ぶため、./bin を PATH の先頭に置いて
# 固定バージョンを解決させる。これをしないと CI（mockgen 未導入）で失敗し、
# ローカルでも各自の PATH 上のバージョン差がそのまま生成物の差になる。
MOCKGEN_VERSION := $(shell go list -m -f '{{.Version}}' go.uber.org/mock)
MOCKGEN_BIN     := $(CURDIR)/bin/mockgen

# 生成物のパス。gen/check の差分検知対象と一致させる。
# //go:generate の -destination を変えたら、ここも必ず更新すること。
GEN_ARTIFACT_PATHS := ':(glob)internal/**/mock/*'

## サーバ起動（デフォルト: info レベル）
run:
	PORT=$(PORT) LOG_LEVEL=$(LOG_LEVEL) go run ./cmd/api

## デバッグレベルでサーバ起動（stdout を logs/<日付>.log にも保存）
run/debug:
	@mkdir -p logs
	PORT=$(PORT) LOG_LEVEL=debug go run ./cmd/api 2>&1 | tee logs/$$(date +%F).log

## outbox-worker 起動（MySQL → Redis へ Outbox イベントを配信）
run/outbox-worker:
	LOG_LEVEL=$(LOG_LEVEL) go run ./cmd/outbox-worker

## テスト実行
test:
	@go test $(TEST_PKGS)

## テスト実行（詳細ログ付き: t.Log と slog 出力を表示）
test/v:
	@go test -v $(TEST_PKGS)

## テスト実行（race 検出 + カバレッジ計測、CI 用 / ローカル任意）
test/race:
	@go test -race -coverprofile=coverage.out $(TEST_PKGS)

## カバレッジを計測して HTML で表示（race 検出なしで coverage.out を生成）
test/cover:
	@go test -coverprofile=coverage.out $(TEST_PKGS)
	go tool cover -html=coverage.out

# 既存の coverage.out を判定するだけで、テストは実行しない。
# CI では test/race が生成したプロファイルをそのまま使い、テストの二重実行を避ける。
# ローカルで単体で叩く場合は先に test/race か test/cover を実行すること
# （プロファイルが古い場合はスクリプトが警告する）。
## 層別カバレッジが閾値を満たすか判定（要 coverage.out。CI 用）
test/cover/check:
	@./scripts/coverage-check.sh coverage.out

## ビルド（./bin/api に出力）
build:
	mkdir -p bin
	go build -o bin/api ./cmd/api

## batch ビルド（./bin/batch に出力）
build/batch:
	mkdir -p bin
	go build -o bin/batch ./cmd/batch

## outbox-worker ビルド（./bin/outbox-worker に出力）
build/outbox-worker:
	mkdir -p bin
	go build -o bin/outbox-worker ./cmd/outbox-worker

## 全バイナリをビルド（api / batch / outbox-worker）
build/all: build build/batch build/outbox-worker

## golangci-lint を .golangci-version のバージョンで ./bin へ導入（既に同一版なら何もしない）
tools/golangci-lint:
	@if [ ! -x "$(GOLANGCI_BIN)" ] || ! "$(GOLANGCI_BIN)" version 2>/dev/null | grep -q "$(patsubst v%,%,$(GOLANGCI_VERSION))"; then \
		echo "installing golangci-lint $(GOLANGCI_VERSION) -> $(GOLANGCI_BIN)"; \
		mkdir -p $(CURDIR)/bin; \
		GOBIN=$(CURDIR)/bin go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_VERSION); \
	fi

## 静的解析（全ファイル対象。CI と同一コマンド）
lint:
	@$(MAKE) --no-print-directory tools/golangci-lint
	$(GOLANGCI_BIN) run ./...

## 静的解析（origin/main からの差分のみ。PR 相当の高速確認用）
lint/diff:
	@$(MAKE) --no-print-directory tools/golangci-lint
	$(GOLANGCI_BIN) run --new-from-rev=origin/main ./...

## 静的解析（自動修正できる指摘を修正する）
lint/fix:
	@$(MAKE) --no-print-directory tools/golangci-lint
	$(GOLANGCI_BIN) run --fix ./...

## mockgen を go.mod の go.uber.org/mock と同じバージョンで ./bin へ導入（既に同一版なら何もしない）
tools/mockgen:
	@if [ ! -x "$(MOCKGEN_BIN)" ] || ! "$(MOCKGEN_BIN)" --version 2>/dev/null | grep -q '^$(MOCKGEN_VERSION)$$'; then \
		echo "installing mockgen $(MOCKGEN_VERSION) -> $(MOCKGEN_BIN)"; \
		mkdir -p $(CURDIR)/bin; \
		GOBIN=$(CURDIR)/bin go install go.uber.org/mock/mockgen@$(MOCKGEN_VERSION); \
	fi

## モック再生成（uber-go/mockgen 使用）
mock/gen:
	@$(MAKE) --no-print-directory tools/mockgen
	PATH="$(CURDIR)/bin:$$PATH" go generate ./...

# determ. §8「生成物と双方向契約検証」の逆方向にあたる。
# 正方向（生成物の要素が実体に実在するか）はコンパイラが担保するため、ここでは
# 逆方向（実体の変更が生成物に反映されているか＝再生成忘れ）だけを見る。
# 変更されたファイルと、新規に生成された未追跡ファイルの両方を検知する。
## 生成物の drift 検知（再生成して差分が出たら失敗。CI 用）
gen/check:
	@$(MAKE) --no-print-directory mock/gen
	@changed=$$(git diff --name-only -- $(GEN_ARTIFACT_PATHS)); \
	added=$$(git ls-files --others --exclude-standard -- $(GEN_ARTIFACT_PATHS)); \
	if [ -n "$$changed" ] || [ -n "$$added" ]; then \
		echo "生成物が最新ではありません。interface を変更したら make mock/gen を実行してコミットしてください。"; \
		echo "--- 差分のあるファイル ---"; \
		[ -n "$$changed" ] && printf '%s\n' "$$changed"; \
		[ -n "$$added" ] && printf '%s (未追跡)\n' "$$added"; \
		git --no-pager diff --stat -- $(GEN_ARTIFACT_PATHS); \
		exit 1; \
	fi; \
	echo "生成物は最新です。"
