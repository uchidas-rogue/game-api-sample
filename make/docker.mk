# Docker 系（本番イメージのビルド / ローカル起動）

.PHONY: docker/build docker/build/all docker/build/migrate docker/run docker/run/grpc docker/run/worker docker/run/batch

## 本番イメージをビルド（BIN=api|batch|grpc|outbox-worker, 既定: api）
docker/build:
	docker build --platform=linux/arm64 --build-arg BIN=$(BIN) -t $(IMAGE_NAME)-$(BIN):$(IMAGE_TAG) .

## 4バイナリ全部のイメージをビルド
docker/build/all:
	$(MAKE) docker/build BIN=api
	$(MAKE) docker/build BIN=batch
	$(MAKE) docker/build BIN=grpc
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

## ローカルで gRPC サーバを起動（ポート9090公開, api と同時起動可）
docker/run/grpc:
	docker run --rm -p $(GRPC_PORT):9090 \
		--env-file .env.docker \
		--name $(IMAGE_NAME)-grpc \
		$(IMAGE_NAME)-grpc:$(IMAGE_TAG)

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
