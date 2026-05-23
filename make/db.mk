# データベース系（sqlc 生成 / マイグレーション / スキーマダンプ）

.PHONY: db/sqlc/gen db/migrate/up db/migrate/down db/migrate/new db/cli db/schema/dump

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
