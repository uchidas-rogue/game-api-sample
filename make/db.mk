# データベース系（sqlc 生成 / マイグレーション / スキーマダンプ）

.PHONY: db/sqlc/gen db/migrate/up db/migrate/down db/migrate/new db/cli db/schema/dump tools/sqlc db/gen/check

# sqlc のバージョンは .sqlc-version 単一箇所で管理する（ローカルと CI の等価性のため）。
# 固定しないと、各自の PATH 上のバージョン差がそのまま生成物の差になり、
# db/gen/check が偽の drift を報告する。
SQLC_VERSION := $(shell cat .sqlc-version)
SQLC_BIN     := $(CURDIR)/bin/sqlc

# sqlc の生成物と、その入力である schema.sql。db/gen/check の差分検知対象。
# 生成物のうち mock/ は mockgen の担当なので除外する（make gen/check が見る）。
# sqlc.yaml の out / schema を変えたら、ここも必ず更新すること。
DB_ARTIFACT_PATHS := deployments/mysql/schema.sql \
                     ':(glob)internal/infrastructure/mysql/sqlc/*.go'

## sqlc を .sqlc-version のバージョンで ./bin へ導入（既に同一版なら何もしない）
tools/sqlc:
	@if [ ! -x "$(SQLC_BIN)" ] || ! "$(SQLC_BIN)" version 2>/dev/null | grep -q '^$(SQLC_VERSION)$$'; then \
		echo "installing sqlc $(SQLC_VERSION) -> $(SQLC_BIN)"; \
		mkdir -p $(CURDIR)/bin; \
		GOBIN=$(CURDIR)/bin go install github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION); \
	fi

## sqlc によるDBアクセスコード生成
db/sqlc/gen:
	@$(MAKE) --no-print-directory tools/sqlc
	$(SQLC_BIN) generate

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

# determ. §8 の逆方向チェックの DB 版。検知するのは2種類の再生成忘れ:
#   1. migrations を足したのに make db/schema/dump を忘れた（schema.sql が古い）
#   2. schema / queries を変えたのに make db/sqlc/gen を忘れた（sqlc 生成物が古い）
# mockgen 生成物は make gen/check の担当。querier.go が変われば
# そちらでモックの再生成忘れも検知される。
## sqlc / schema.sql の drift 検知（再生成して差分が出たら失敗。CI 用）
db/gen/check:
	@$(MAKE) --no-print-directory db/schema/dump
	@$(MAKE) --no-print-directory db/sqlc/gen
	@changed=$$(git diff --name-only -- $(DB_ARTIFACT_PATHS)); \
	added=$$(git ls-files --others --exclude-standard -- $(DB_ARTIFACT_PATHS)); \
	if [ -n "$$changed" ] || [ -n "$$added" ]; then \
		echo "DB 生成物が最新ではありません。make db/schema/dump と make db/sqlc/gen を実行してコミットしてください。"; \
		echo "--- 差分のあるファイル ---"; \
		[ -n "$$changed" ] && printf '%s\n' "$$changed"; \
		[ -n "$$added" ] && printf '%s (未追跡)\n' "$$added"; \
		git --no-pager diff --stat -- $(DB_ARTIFACT_PATHS); \
		exit 1; \
	fi; \
	echo "DB 生成物は最新です。"
