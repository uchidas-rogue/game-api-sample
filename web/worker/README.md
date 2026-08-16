# チャット中継（Cloudflare Workers）

サイトのチャットが呼ぶ中継サーバ。Anthropic の API キーを持ち、リポジトリの索引から本文を解決して
モデルへ渡す。**デプロイは手動**（Cloudflare のトークンを GitHub に置かないため）。

## 何を受け取り、何を受け取らないか

```
POST /  { "question": "…", "chunkIds": ["AGENTS.md#…", …] }
```

**本文はクライアントから受け取らない。** 受け取るのはチャンク ID だけで、本文は Worker にバンドルされた
`web/data/index.json` から解決する。任意テキストを送り込んで無料の LLM として使われる経路を塞ぐため。

## 防御

| 対策 | 値 | 場所 |
|---|---|---|
| Origin チェック | `ALLOWED_ORIGINS` + localhost | `wrangler.toml` の `[vars]` |
| IP ごとのレート制限 | 10 req/min | `RATE_PER_MINUTE` |
| IP ごとの日次上限 | 30 req/day | `DAILY_CAP_PER_IP` |
| サイト全体の日次上限 | 200 req/day | `DAILY_CAP` |
| 質問文の長さ | 500 文字 | 索引の `limits.maxQuestionChars` |
| 参照チャンク数 | 6 | 索引の `limits.topK` |
| 応答トークン | 1024 | `MAX_TOKENS` |

質問文の長さと参照チャンク数はブラウザ側も同じ値で弾く。**正本は `scripts/sitegen` の const** で、
索引（`web/data/index.json`）に載せて両方に読ませている。2つのランタイムへ同じ数字を書き写すと
片方だけ古くなるため。Worker も同じ索引をバンドルするので、生成物を1つ配れば原理的にズレない。

**Origin チェックは非ブラウザには効かない。** `Origin` ヘッダを付けない相手（curl 等）は素通りする。
他サイトに貼られたページからの利用を防ぐのが目的で、直接叩かれることは防げない。
直接叩く相手に対して効くのは、この下のレート制限・日次上限と、「本文を受け取らない」設計のほう。

**日次上限は IP 別とサイト全体の二段。** サイト全体の上限だけだと、毎分上限で叩き続ける1人が
20 分（10 req/min × 20 = 200）でその日の枠を使い切れてしまう。IP 別の上限を併せて、
全体の枠を食い潰すのに最低 7 IP 必要になるようにしている。

**KV 書き込みは上限で頭打ちになる。** カウンタは「全部 get して判定 → 通ったものだけ put」の順で
更新する。弾いたリクエストは書き込まないので、書き込みは 200 × 3 キー = 600 write/day が上限
（無料枠は 1,000 write/day）。判定より前に put していると、枠を使い切った後も書き込みだけが増え続け、
無料枠が枯れた時点でレート制限そのものが黙って効かなくなる。

### コスト

モデルは `claude-haiku-4-5`（$1 / $5 per MTok）。索引のチャンク上限（1,200 文字）が効くので、
モデルへ渡る入力は上位 6 件で最大 7 千文字強に収まる。

| | 入力 | 出力 | 1問あたり | 日次上限まで使うと |
|---|---|---|---|---|
| 典型（平均 487 文字 × 6 件） | 約 3.2k tok | 約 300 tok | 約 0.7 円 | 約 140 円/日 |
| 最悪（上限の 6 件 + 応答上限） | 約 7.3k tok | 1,024 tok | 約 1.9 円 | 約 380 円/日 |

日次上限の 200 はこの最悪値を基準に置いている。実測は初回デプロイ後に `usage` を見て見直す。

## セットアップ

```bash
cd web/worker
npm install

# KV（レート制限・日次上限のカウンタ置き場）を作り、出力された id を wrangler.toml へ
npx wrangler kv namespace create CHAT_KV

# API キーは secret として登録（wrangler.toml には絶対に書かない）
npx wrangler secret put ANTHROPIC_API_KEY
```

## ローカル実行

```bash
cp .dev.vars.example .dev.vars   # 自分のキーを入れる（.gitignore 済み）
npx wrangler dev                 # http://localhost:8787
```

サイト側は `web/index.html` の `<meta name="chat-endpoint">` にこの URL を入れると繋がる。
未設定のままでも、サイトは検索でヒットした文書の抜粋表示にフォールバックする。

## デプロイ

```bash
npx wrangler deploy
```

デプロイ後に出る `https://game-api-sample-chat.<subdomain>.workers.dev` を
`web/index.html` の `<meta name="chat-endpoint">` に設定してコミットする。

## 索引を更新したら

索引は Worker にバンドルされる。文書を変更したら **`make site/gen` の後に再デプロイ**しないと、
サイト（GitHub Pages 側の索引）と Worker（バンドル済みの索引）で ID がずれ、
「参照する文書を特定できませんでした」が返るようになる。
