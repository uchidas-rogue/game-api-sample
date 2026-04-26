PORT      ?= 8080
LOG_LEVEL ?= info

.PHONY: run run/debug test build lint mock/gen

## サーバ起動（デフォルト: info レベル）
run:
	PORT=$(PORT) LOG_LEVEL=$(LOG_LEVEL) go run ./cmd/api

## デバッグレベルでサーバ起動（logs/ にもファイル出力）
run/debug:
	PORT=$(PORT) LOG_LEVEL=debug go run ./cmd/api

## テスト実行
test:
	go test ./...

## ビルド（./bin/api に出力）
build:
	mkdir -p bin
	go build -o bin/api ./cmd/api

## 静的解析
lint:
	go vet ./...

## モック再生成（uber-go/mockgen 使用）
mock/gen:
	go generate ./...
