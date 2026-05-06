PORT      ?= 8080
LOG_LEVEL ?= info

MIGRATE_DSN ?= mysql://game:game@tcp(127.0.0.1:3306)/game_db?multiStatements=true

.PHONY: help run run/debug run/outbox-worker test test/v build build/batch build/outbox-worker build/all lint mock/gen db/sqlc/gen db/migrate/up db/migrate/down db/migrate/new db/schema/dump db/cli

.DEFAULT_GOAL := help

## このヘルプを表示
help:
	@awk 'BEGIN {FS = ":.*##"; printf "Usage:\n  make <target>\n\nTargets:\n"} /^[a-zA-Z0-9_\/-]+:.*?##/ { printf "  %-20s %s\n", $$1, $$2 } /^## / { sub(/^## /, "", $$0); doc=$$0 } /^[a-zA-Z0-9_\/-]+:/ && doc { printf "  %-20s %s\n", $$1, doc; doc="" }' $(MAKEFILE_LIST)

## サーバ起動（デフォルト: info レベル）
run:
	PORT=$(PORT) LOG_LEVEL=$(LOG_LEVEL) go run ./cmd/api

## デバッグレベルでサーバ起動（logs/ にもファイル出力）
run/debug:
	PORT=$(PORT) LOG_LEVEL=debug go run ./cmd/api

## outbox-worker 起動（MySQL → Redis へ Outbox イベントを配信）
run/outbox-worker:
	LOG_LEVEL=$(LOG_LEVEL) go run ./cmd/outbox-worker

## テスト実行
test:
	go test ./...

## テスト実行（詳細ログ付き: t.Log と slog 出力を表示）
test/v:
	go test -v ./...

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

## sqlc によるDBアクセスコード生成
db/sqlc/gen:
	sqlc generate

## マイグレーション適用（golang-migrate）
db/migrate/up:
	migrate -database "$(MIGRATE_DSN)" -path deployments/mysql/migrations up

## マイグレーション1段階ロールバック
db/migrate/down:
	migrate -database "$(MIGRATE_DSN)" -path deployments/mysql/migrations down 1

## 新規マイグレーションファイル作成（使用例: make db/migrate/new name=create_user_tables）
db/migrate/new:
	@if [ -z "$(name)" ]; then echo "Error: name is required. Usage: make db/migrate/new name=xxx"; exit 1; fi
	migrate create -ext sql -dir deployments/mysql/migrations -seq $(name)

## MySQL コンテナに接続（utf8mb4 + LANG 明示で日本語入力対応）
db/cli:
	docker compose -f deployments/docker-compose.yml exec -it \
		-e MYSQL_PWD=game -e LANG=C.UTF-8 -e LC_ALL=C.UTF-8 \
		mysql mysql --default-character-set=utf8mb4 -ugame game_db

## migrations/*.up.sql を結合して schema.sql を再生成（sqlc 用）
db/schema/dump:
	@echo "-- このファイルは make db/schema/dump で自動生成されます。直接編集しないでください" > deployments/mysql/schema.sql
	@for f in $$(ls deployments/mysql/migrations/*.up.sql | sort); do \
		echo "" >> deployments/mysql/schema.sql; \
		echo "-- from $$(basename $$f)" >> deployments/mysql/schema.sql; \
		cat $$f >> deployments/mysql/schema.sql; \
	done
	@echo "schema.sql を再生成しました"
