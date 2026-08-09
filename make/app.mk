# Go アプリ開発系（起動 / テスト / ビルド / 静的解析 / モック生成）

.PHONY: run run/debug run/outbox-worker test test/v test/race test/cover build build/batch build/outbox-worker build/all lint lint/diff lint/fix tools/golangci-lint mock/gen

# golangci-lint のバージョンは .golangci-version 単一箇所で管理する（ローカルと CI の等価性のため）。
# go.mod の tool ディレクティブは採用しない: golangci-lint の依存がアプリ本体の
# モジュールグラフに混ざり、golang.org/x/text 等の共有依存を巻き上げてしまうため。
GOLANGCI_VERSION := $(shell cat .golangci-version)
GOLANGCI_BIN     := $(CURDIR)/bin/golangci-lint

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

## モック再生成（uber-go/mockgen 使用）
mock/gen:
	go generate ./...
