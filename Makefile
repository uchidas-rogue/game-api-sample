# 開発コマンドの統一エントリポイント。
# ターゲット定義は作業ドメインごとに make/*.mk へ分割している。
# このファイルは「変数定義 + help + include」に集約する。

PORT      ?= 8080
LOG_LEVEL ?= info

MIGRATE_DSN ?= mysql://game:game@tcp(127.0.0.1:3306)/game_db?multiStatements=true

IMAGE_NAME ?= game-api
IMAGE_TAG  ?= latest
BIN        ?= api

# Terraform backend（state 保管先）のブートストラップ用。
# バケット名は <prefix>-<アカウントID> で、main.tf の locals と一致させる。
AWS_REGION     ?= ap-northeast-1
TFSTATE_PREFIX ?= game-api-tfstate

# テスト対象外パッケージは .testignore で宣言する（除外理由もファイル内に併記）。
# - コメント行（#）と空行を除いた各行を | で連結して 1 本の正規表現にする
# - ファイルが空 / 欠損のときに `grep -vE ''` が全行除外する footgun を踏まないよう、
#   先頭に必ずマッチしない sentinel を挟む
TEST_IGNORE_FILE := .testignore
TEST_EXCLUDE_RE  := $(shell { echo '^__never_match__$$'; grep -vE '^[[:space:]]*(\#|$$)' $(TEST_IGNORE_FILE) 2>/dev/null; } | tr '\n' '|' | sed 's/|$$//')
TEST_PKGS        := $(shell go list ./... | grep -vE '$(TEST_EXCLUDE_RE)')

.PHONY: help

.DEFAULT_GOAL := help

## このヘルプを表示
help:
	@awk 'BEGIN {FS = ":.*##"; printf "Usage:\n  make <target>\n\nTargets:\n"} /^[a-zA-Z0-9_\/-]+:.*?##/ { printf "  %-20s %s\n", $$1, $$2 } /^## / { sub(/^## /, "", $$0); doc=$$0 } /^[a-zA-Z0-9_\/-]+:/ && doc { printf "  %-20s %s\n", $$1, doc; doc="" }' $(MAKEFILE_LIST)

include make/app.mk
include make/db.mk
include make/docker.mk
include make/terraform.mk
include make/loadtest.mk
