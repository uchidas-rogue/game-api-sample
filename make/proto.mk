# Protocol Buffers / gRPC 系（生成 / lint / 後方互換チェック）。
#
# buf を採用している理由:
#   - 単一バイナリを go install で導入できるため、.golangci-version / .sqlc-version と
#     同じ「バージョンを 1 箇所で固定して ./bin へ入れる」流儀にそのまま乗る
#   - buf breaking が proto の後方互換チェックをそのまま満たす。gRPC では .proto 自体が
#     レスポンス契約の正本なので、HTTP 側の testdata/contracts/*.json に相当する仕組みを
#     別途作る必要がない（AGENTS.md §3）

.PHONY: tools/buf tools/protoc-plugins proto/gen proto/gen/csharp proto/gen/check proto/lint proto/breaking

BUF_VERSION := $(shell cat .buf-version)
BUF_BIN     := $(CURDIR)/bin/buf

# protoc-gen-go のバージョンは go.mod の google.golang.org/protobuf から導出する
# （別ファイルに二重管理しない。mockgen と同じ考え方）。
PROTOC_GEN_GO_VERSION := $(shell go list -m -f '{{.Version}}' google.golang.org/protobuf)
PROTOC_GEN_GO_BIN     := $(CURDIR)/bin/protoc-gen-go

# protoc-gen-go-grpc は google.golang.org/grpc とは別モジュール（grpc/cmd/protoc-gen-go-grpc）で
# go.mod に現れないため、バージョンを専用ファイルで固定する。
PROTOC_GEN_GO_GRPC_VERSION := $(shell cat .protoc-gen-go-grpc-version)
PROTOC_GEN_GO_GRPC_BIN     := $(CURDIR)/bin/protoc-gen-go-grpc

PROTO_DIR := proto

# Go 生成物のパス。proto/gen/check の差分検知対象と一致させる。
# buf.gen.yaml の out / module を変えたら、ここと .golangci.yml の除外パス、
# .testignore も併せて更新すること（この連動は proto/gen/check では検知できない）。
PROTO_ARTIFACT_PATHS := ':(glob)internal/driver/grpc/gen/**'

# .proto の FileDescriptorSet のダイジェスト。C# 生成物が .proto に追随しているかの判定に使う。
PROTO_DIGEST_FILE := clients/unity/.proto-digest

## buf を .buf-version のバージョンで ./bin へ導入（既に同一版なら何もしない）
tools/buf:
	@if [ ! -x "$(BUF_BIN)" ] || ! "$(BUF_BIN)" --version 2>/dev/null | grep -q '^$(patsubst v%,%,$(BUF_VERSION))$$'; then \
		echo "installing buf $(BUF_VERSION) -> $(BUF_BIN)"; \
		mkdir -p $(CURDIR)/bin; \
		GOBIN=$(CURDIR)/bin go install github.com/bufbuild/buf/cmd/buf@$(BUF_VERSION); \
	fi

## protoc プラグイン（protoc-gen-go / protoc-gen-go-grpc）を固定バージョンで ./bin へ導入
tools/protoc-plugins:
	@if [ ! -x "$(PROTOC_GEN_GO_BIN)" ] || ! "$(PROTOC_GEN_GO_BIN)" --version 2>/dev/null | grep -q '$(PROTOC_GEN_GO_VERSION)$$'; then \
		echo "installing protoc-gen-go $(PROTOC_GEN_GO_VERSION) -> $(PROTOC_GEN_GO_BIN)"; \
		mkdir -p $(CURDIR)/bin; \
		GOBIN=$(CURDIR)/bin go install google.golang.org/protobuf/cmd/protoc-gen-go@$(PROTOC_GEN_GO_VERSION); \
	fi
	@if [ ! -x "$(PROTOC_GEN_GO_GRPC_BIN)" ] || ! "$(PROTOC_GEN_GO_GRPC_BIN)" --version 2>/dev/null | grep -q '$(patsubst v%,%,$(PROTOC_GEN_GO_GRPC_VERSION))$$'; then \
		echo "installing protoc-gen-go-grpc $(PROTOC_GEN_GO_GRPC_VERSION) -> $(PROTOC_GEN_GO_GRPC_BIN)"; \
		mkdir -p $(CURDIR)/bin; \
		GOBIN=$(CURDIR)/bin go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@$(PROTOC_GEN_GO_GRPC_VERSION); \
	fi

## Go の gRPC コードを生成（ローカルプラグインのみ。ネットワーク不要）
proto/gen:
	@$(MAKE) --no-print-directory tools/buf
	@$(MAKE) --no-print-directory tools/protoc-plugins
	PATH="$(CURDIR)/bin:$$PATH" $(BUF_BIN) generate $(PROTO_DIR) --template $(PROTO_DIR)/buf.gen.yaml

# C# は buf.build（BSR）の remote plugin で生成するためネットワークが要る。
# Go 生成と分けているのは、CI の必須ゲートに外部サービスへの到達性を持ち込まないため。
# 生成後に .proto のダイジェストを記録し、proto/gen/check がこれを突合する。
## Unity クライアント向けの C# コードを生成（要ネットワーク。生成物はコミットする）
proto/gen/csharp:
	@$(MAKE) --no-print-directory tools/buf
	@mkdir -p clients/unity/Runtime/Generated
	$(BUF_BIN) generate $(PROTO_DIR) --template $(PROTO_DIR)/buf.gen.csharp.yaml
	@$(BUF_BIN) build $(PROTO_DIR) -o - | shasum -a 256 | awk '{print $$1}' > $(PROTO_DIGEST_FILE)
	@echo "C# 生成完了。ダイジェストを $(PROTO_DIGEST_FILE) に記録しました。"

# determ. §8「生成物と双方向契約検証」の proto 版。2 つを見る:
#   1. Go 生成物: 再生成して git diff が出たら再生成忘れ（mockgen の gen/check と同じ流儀）
#   2. C# 生成物: .proto のダイジェストが記録値と違えば、.proto だけ変えて
#      make proto/gen/csharp を忘れている
#
# 【2 の既知の検出漏れ】BSR 側のプラグイン版が上がっただけの場合はダイジェストが変わらないため、
# C# 生成物が古いままでも通る。remote plugin をローカルへ持ち込まない限り原理的に検知できない
# （検知するには生成物そのものを CI で再生成する必要があり、必須ゲートが外部サービスに依存する）。
## proto 生成物の drift 検知（CI 用）
proto/gen/check:
	@$(MAKE) --no-print-directory proto/gen
	@changed=$$(git diff --name-only -- $(PROTO_ARTIFACT_PATHS)); \
	added=$$(git ls-files --others --exclude-standard -- $(PROTO_ARTIFACT_PATHS)); \
	if [ -n "$$changed" ] || [ -n "$$added" ]; then \
		echo "Go の proto 生成物が最新ではありません。.proto を変更したら make proto/gen を実行してコミットしてください。"; \
		[ -n "$$changed" ] && printf '%s\n' "$$changed"; \
		[ -n "$$added" ] && printf '%s (未追跡)\n' "$$added"; \
		exit 1; \
	fi
	@$(MAKE) --no-print-directory tools/buf
	@if [ ! -f "$(PROTO_DIGEST_FILE)" ]; then \
		echo "$(PROTO_DIGEST_FILE) がありません。make proto/gen/csharp を実行してコミットしてください。"; \
		exit 1; \
	fi
	@current=$$($(BUF_BIN) build $(PROTO_DIR) -o - | shasum -a 256 | awk '{print $$1}'); \
	recorded=$$(cat $(PROTO_DIGEST_FILE)); \
	if [ "$$current" != "$$recorded" ]; then \
		echo "C# 生成物が .proto に追随していません。make proto/gen/csharp を実行してコミットしてください。"; \
		echo "  現在の .proto: $$current"; \
		echo "  記録値       : $$recorded"; \
		exit 1; \
	fi
	@echo "proto 生成物は最新です。"

## proto の命名規約チェック
proto/lint:
	@$(MAKE) --no-print-directory tools/buf
	$(BUF_BIN) lint $(PROTO_DIR)

# 比較元は必ず origin/ 付きで書く。actions/checkout は対象 ref のローカルブランチしか作らず、
# 裸の `main`（refs/heads/main）は CI に存在しない。git の revision 解決は
# refs/heads/ → refs/remotes/<名前> の順で、refs/remotes/origin/main は `main` では引けない。
# fetch-depth: 0 が要るのは「main を解決するため」ではなく origin/main を fetch させるため。
#
# 捕捉した実例: 以前ここが裸の `main` だったため、CI では条件が常に false になり
# buf breaking が一度も実行されないまま「初回導入時」の分岐に落ちて緑になっていた
# （PR #26 の CI ログで確認）。ローカルには refs/heads/main が実在するので手元では露見しない。
# determ. §7「ローカルとCIの等価性」に反する形だったため、解決できない場合は落とす。
## proto の後方互換チェック（origin/main との比較）
proto/breaking:
	@$(MAKE) --no-print-directory tools/buf
	@git rev-parse --verify --quiet origin/main >/dev/null || { \
		echo "比較元 origin/main を解決できません。"; \
		echo "CI では actions/checkout に fetch-depth: 0 が必要です（既定の shallow clone では fetch されない）。"; \
		exit 1; \
	}
	@if git ls-tree -r --name-only origin/main -- $(PROTO_DIR) | grep -q .; then \
		$(BUF_BIN) breaking $(PROTO_DIR) --against '.git#branch=origin/main,subdir=$(PROTO_DIR)'; \
	else \
		echo "origin/main に $(PROTO_DIR)/ が無いためスキップします（初回導入時）。"; \
	fi
