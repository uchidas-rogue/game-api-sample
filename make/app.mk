# Go アプリ開発系（起動 / テスト / ビルド / 静的解析 / モック生成）

.PHONY: run run/debug run/outbox-worker test test/v test/race test/cover build build/batch build/outbox-worker build/all lint mock/gen

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

## 静的解析
lint:
	go vet ./...

## モック再生成（uber-go/mockgen 使用）
mock/gen:
	go generate ./...
