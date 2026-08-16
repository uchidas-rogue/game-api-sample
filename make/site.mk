# ポートフォリオサイト（GitHub Pages）の索引生成。
#
# サイトのチャットは web/data/index.json を唯一の根拠にする。索引はリポジトリの文書
# （AGENTS.md / ROADMAP.md / docs/** / terraform/ARCHITECTURE.md 等）から生成するので、
# 文書を直したら再生成が要る。再生成漏れは site/check が差分で検知する（gen/check と同じ流儀）。

.PHONY: site/gen site/check site/serve

## サイトの知識源（web/data/index.json）を文書から再生成する
site/gen:
	@go run ./scripts/sitegen

## サイト索引の drift 検知（再生成して既存と食い違ったら失敗。CI 用）
#
# 一時ファイルへ生成して cmp で比べる。ワークツリーの索引を書き換えないので、
# 検査そのものが read-only になる。
# 以前は site/gen で上書きしてから git status を見ていたが、それだと
# 「まだコミットしていない新規の索引」も未更新と報告してしまい、
# 内容が最新かどうかとは無関係に失敗していた。
site/check:
	@tmp=$$(mktemp -d) && trap 'rm -rf "$$tmp"' EXIT; \
	go run ./scripts/sitegen -o "$$tmp/index.json" >/dev/null; \
	if ! cmp -s "$$tmp/index.json" web/data/index.json; then \
		echo "web/data/index.json が最新ではありません。make site/gen を実行して結果をコミットしてください。"; \
		diff -u web/data/index.json "$$tmp/index.json" | head -40; \
		exit 1; \
	fi; \
	echo "サイト索引は最新です。"

## サイトをローカルで配信する（http://localhost:8000）
site/serve:
	@python3 -m http.server 8000 --directory web
