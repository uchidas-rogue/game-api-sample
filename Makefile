PORT      ?= 8080
LOG_LEVEL ?= info

MIGRATE_DSN ?= mysql://game:game@tcp(127.0.0.1:3306)/game_db?multiStatements=true

IMAGE_NAME ?= game-api
IMAGE_TAG  ?= latest
BIN        ?= api

# テスト対象外パッケージは .testignore で宣言する（除外理由もファイル内に併記）。
# - コメント行（#）と空行を除いた各行を | で連結して 1 本の正規表現にする
# - ファイルが空 / 欠損のときに `grep -vE ''` が全行除外する footgun を踏まないよう、
#   先頭に必ずマッチしない sentinel を挟む
TEST_IGNORE_FILE := .testignore
TEST_EXCLUDE_RE  := $(shell { echo '^__never_match__$$'; grep -vE '^[[:space:]]*(\#|$$)' $(TEST_IGNORE_FILE) 2>/dev/null; } | tr '\n' '|' | sed 's/|$$//')
TEST_PKGS        := $(shell go list ./... | grep -vE '$(TEST_EXCLUDE_RE)')

.PHONY: help run run/debug run/outbox-worker test test/v test/race test/cover build build/batch build/outbox-worker build/all lint mock/gen db/sqlc/gen db/migrate/up db/migrate/down db/migrate/new db/schema/dump db/cli docker/build docker/build/all docker/build/migrate docker/run docker/run/worker docker/run/batch

.DEFAULT_GOAL := help

## このヘルプを表示
help:
	@awk 'BEGIN {FS = ":.*##"; printf "Usage:\n  make <target>\n\nTargets:\n"} /^[a-zA-Z0-9_\/-]+:.*?##/ { printf "  %-20s %s\n", $$1, $$2 } /^## / { sub(/^## /, "", $$0); doc=$$0 } /^[a-zA-Z0-9_\/-]+:/ && doc { printf "  %-20s %s\n", $$1, doc; doc="" }' $(MAKEFILE_LIST)

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

test/cover: test/race ## カバレッジを HTML で表示（test/race を実行してからブラウザで開く）
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

## 本番イメージをビルド（BIN=api|batch|outbox-worker, 既定: api）
docker/build:
	docker build --platform=linux/arm64 --build-arg BIN=$(BIN) -t $(IMAGE_NAME)-$(BIN):$(IMAGE_TAG) .

## 3バイナリ全部のイメージをビルド
docker/build/all:
	$(MAKE) docker/build BIN=api
	$(MAKE) docker/build BIN=batch
	$(MAKE) docker/build BIN=outbox-worker

## golang-migrate + migrations 同梱の専用イメージをビルド（ECS RunTask 起動用）
docker/build/migrate:
	docker build --platform=linux/arm64 -f Dockerfile.migrate -t $(IMAGE_NAME)-migrate:$(IMAGE_TAG) .

## ローカルで本番APIイメージを起動（ポート8080公開, .env.docker を読み込み）
docker/run:
	docker run --rm -p $(PORT):8080 \
		--env-file .env.docker \
		--name $(IMAGE_NAME)-api \
		$(IMAGE_NAME)-api:$(IMAGE_TAG)

## ローカルで outbox-worker を起動（ポート公開なし, api と同時起動可）
docker/run/worker:
	docker run --rm \
		--env-file .env.docker \
		--name $(IMAGE_NAME)-outbox-worker \
		$(IMAGE_NAME)-outbox-worker:$(IMAGE_TAG)

## ローカルで batch を起動（ポート公開なし, ワンショット想定）
docker/run/batch:
	docker run --rm \
		--env-file .env.docker \
		--name $(IMAGE_NAME)-batch \
		$(IMAGE_NAME)-batch:$(IMAGE_TAG)

## migrations/*.up.sql を結合して schema.sql を再生成（sqlc 用）
db/schema/dump:
	@echo "-- このファイルは make db/schema/dump で自動生成されます。直接編集しないでください" > deployments/mysql/schema.sql
	@for f in $$(ls deployments/mysql/migrations/*.up.sql | sort); do \
		echo "" >> deployments/mysql/schema.sql; \
		echo "-- from $$(basename $$f)" >> deployments/mysql/schema.sql; \
		cat $$f >> deployments/mysql/schema.sql; \
	done
	@echo "schema.sql を再生成しました"
