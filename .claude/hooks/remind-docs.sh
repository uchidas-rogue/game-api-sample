#!/usr/bin/env bash
# Stop hook: ターン終了時、コード/インフラ変更があるのに資料が未更新ならリマインドする。
# AGENTS.md の Infrastructure Change Rules / アーキテクチャ規約と実装の乖離を防ぐ目的。
set -euo pipefail

input=$(cat)

# stop_hook_active が true の場合は、このリマインド由来の再停止。ループ防止のため何もしない。
if [ "$(printf '%s' "$input" | jq -r '.stop_hook_active // false')" = "true" ]; then
  exit 0
fi

cwd=$(printf '%s' "$input" | jq -r '.cwd // "."')
cd "$cwd" 2>/dev/null || exit 0

changed=$(git status --porcelain 2>/dev/null || true)
[ -z "$changed" ] && exit 0

# コード・インフラ・規約設定の変更
# （.go / .tf / ワークフロー yaml / Makefile / lint 設定 / テスト除外設定 / スクリプト）
# lint 設定とテスト除外設定は「規約の正本」なので、変更時は資料との整合確認が必要。
code_changed=$(printf '%s\n' "$changed" \
  | grep -E '\.(go|tf)$|\.github/workflows/.*\.ya?ml$|(^|/)Makefile$|make/.*\.mk$|(^|/)\.golangci(\.yml|-version)$|(^|/)\.sqlc-version$|(^|/)sqlc\.yaml$|(^|/)\.testignore$|scripts/.*\.sh$' || true)

# 資料の変更（AGENTS.md / CLAUDE.md / ARCHITECTURE.md / ROADMAP.md / テスト設計文書）
docs_changed=$(printf '%s\n' "$changed" \
  | grep -E '(AGENTS|CLAUDE|ARCHITECTURE|ROADMAP)\.md|docs/testing/.*\.md' || true)

# usecase / driver の実装変更は、対応する設計図（docs/testing/）の更新要否を必ず確認させる。
# 更新が必須になる4トリガの正本は docs/testing/README.md §5。
flow_code_changed=$(printf '%s\n' "$changed" \
  | grep -E 'internal/(usecase|driver)/.*\.go$' | grep -v '_test\.go$' || true)
# 「設計図」として数えるのは docs/testing/ 直下の機能別ファイルのみ。
# principles/（原則）と README.md（運用ルール）は設計図ではないので、
# これらを触っただけでブロックが解除されないようにする。
testing_docs_changed=$(printf '%s\n' "$changed" \
  | grep -E 'docs/testing/[^/]+\.md$' | grep -v 'docs/testing/README\.md' || true)

if [ -n "$flow_code_changed" ] && [ -z "$testing_docs_changed" ]; then
  jq -n '{
    decision: "block",
    reason: "usecase / driver の実装を変更していますが docs/testing/ の設計図・テスト仕様表が未更新です。処理ノードの追加削除・分岐条件の変更・下位層への呼び出し内容の変更・戻り値の変更のいずれかに該当する場合、図とテスト仕様表の更新は必須です（docs/testing/README.md §5）。該当しない変更であれば、その旨を一言述べて終了してください。"
  }'
  exit 0
fi

if [ -n "$code_changed" ] && [ -z "$docs_changed" ]; then
  jq -n '{
    decision: "block",
    reason: "コード/インフラの変更がありますが AGENTS.md / CLAUDE.md / terraform/ARCHITECTURE.md / ROADMAP.md が未更新です。変更内容がこれらの資料の記述（アーキテクチャ規約・Infrastructure Change Rules・構成図・ロードマップなど）と矛盾しないか確認し、必要なら更新してください。資料更新が不要な変更であれば、その旨を一言述べて終了してください。"
  }'
fi
exit 0
