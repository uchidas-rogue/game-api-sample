# レビュー用の差分取得。生成物を除外する。
#
# なぜ要るか（捕捉した実例）:
#   PR #26（gRPC delivery 追加）で Claude Code Review が1件もコメントを投稿せずに終了した。
#   `gh pr diff` の出力 780KB のうち 51.4% が生成物で、しかも clients/ はアルファベット順で
#   cmd/ configs/ internal/ より前に来る。Bash ツールの読み取り窓（既定 30,000 文字、
#   上限 150,000）は生成 C# で埋まり、最初の .go は 210,000 バイト目。上限まで上げても
#   Go コードに1行も到達しないままレビューが終わっていた。
#
# 除外はパス列挙ではなく生成マーカー（DO NOT EDIT）で判定する。
# パスで持つと、書式の違う既存の除外リスト（make/app.mk の GEN_ARTIFACT_PATHS・
# make/db.mk の DB_ARTIFACT_PATHS・make/proto.mk の PROTO_ARTIFACT_PATHS・
# .golangci.yml の exclusions.paths・.testignore）に、整合を機械判定できないまま
# もう1つ足すことになる。同じ理由でマーカー判定を選んだ前例が
# scripts/archcheck/ifaceassert.go の CheckIfaceAssert にある。

.PHONY: review/files review/diff/stat review/diff

# REVIEW_BASE は比較元。CI からは PR の base ブランチを渡す。
REVIEW_BASE  ?= origin/main
# REVIEW_PATHS は読む範囲。大きな PR は領域ごとに分けて引く。
REVIEW_PATHS ?= .

# web/data/index.json は JSON でコメントが書けず生成マーカーを持てないため、
# 唯一ここだけ名指しで除外する。生成元は scripts/sitegen（make site/gen）。
REVIEW_MARKERLESS := web/data/index.json

# REVIEW_BASE が解決できるかを先に確かめる。CI の checkout が shallow（fetch-depth: 1）だと
# origin/main が無く、git diff が "unknown revision" で落ちる。原因が分かる形で止める。
# 各レシピの先頭に置く（prerequisite にすると make help の表示が崩れる）。
REVIEW_BASE_GUARD = git rev-parse --verify --quiet $(REVIEW_BASE) >/dev/null || { \
		echo "REVIEW_BASE=$(REVIEW_BASE) を解決できません。"; \
		echo "CI では actions/checkout に fetch-depth: 0 が必要です（既定の shallow clone では base を解決できない）。"; \
		exit 1; \
	}

# 生成物を除いた変更ファイル一覧を出すシェル片。3ターゲットで共有する。
# 削除されたファイルは worktree に無くマーカーを読めないので、除外せず残す
# （消えた生成物が1行の削除として出るだけで、読み取り窓を圧迫しない）。
# case 文は使わない。$$(...) の中に置くと閉じ括弧が command substitution の終端と
# 誤認され sh のパースが壊れる（実際に踏んだ）。
#
# 【既知の死角】マーカーを持つ手書きファイルも除外される。現時点で該当するのは
# scripts/archcheck/testdata/skip_generated.go だけで、これは archcheck の
# 「生成物を飛ばす」判定を検証するための、マーカーを持つこと自体が目的のフィクスチャ。
# 除外リストへ足して打ち消すとパス列挙が復活するので、死角として記録に留める。
REVIEW_FILES_CMD = git diff $(REVIEW_BASE)...HEAD --name-only -- $(REVIEW_PATHS) \
	| grep -vxF $(addprefix -e ,$(REVIEW_MARKERLESS)) \
	| while read -r f; do \
		if [ -f "$$f" ] && head -5 "$$f" 2>/dev/null | grep -qi 'DO NOT EDIT'; then continue; fi; \
		printf '%s\n' "$$f"; \
	done

## レビュー対象の変更ファイル一覧（生成物を除外）
review/files:
	@$(REVIEW_BASE_GUARD)
	@$(REVIEW_FILES_CMD)

## レビュー対象の変更概要（生成物を除外。まずこれで全体像を掴む）
review/diff/stat:
	@$(REVIEW_BASE_GUARD)
	@files=$$($(REVIEW_FILES_CMD)); \
	if [ -z "$$files" ]; then echo "レビュー対象の変更はありません。"; exit 0; fi; \
	git diff $(REVIEW_BASE)...HEAD --stat -- $$files

## レビュー対象の差分（生成物を除外。範囲は REVIEW_PATHS='internal/driver' のように絞る）
review/diff:
	@$(REVIEW_BASE_GUARD)
	@files=$$($(REVIEW_FILES_CMD)); \
	if [ -z "$$files" ]; then echo "レビュー対象の変更はありません。"; exit 0; fi; \
	git diff $(REVIEW_BASE)...HEAD -- $$files
