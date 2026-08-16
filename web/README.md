# web — ポートフォリオサイト（GitHub Pages）

リポジトリの概要と設計判断を見せる静的サイト。「このリポジトリについて質問できるチャット」を持つ。
ビルドツールは使わない（素の HTML / CSS / JS のみ。外部 CDN にも依存しない）。

```
web/
  index.html      サイト本体
  styles.css
  app.js          索引の検索（BM25）とチャットのクライアント
  data/index.json 知識源（make site/gen の生成物。手で編集しない）
  worker/         チャット中継（Cloudflare Workers。Pages には配信しない）
```

## 知識源は文書から自動生成する

`data/index.json` は `scripts/sitegen` が文書を見出し単位に分割して生成する（取り込み対象の正本は
`scripts/sitegen` の `rootDocs()` と `docs/**`）。**チャットの根拠にサイト専用の説明文を別途書かない**のは、
同じ情報を2箇所に置くと片方だけ古くなるため（このリポジトリが一貫して避けている失敗形）。

上限値（`limits.topK` / `limits.maxQuestionChars`）も索引に載せてある。ブラウザとプロキシが
同じ索引から読むので、2つのランタイムへ同じ数字を書き写す必要がない。**正本は `scripts/sitegen` の const**。

### カード本文だけは手書き（方針）

`index.html` のカード本文は例外で、採用担当向けの要約として手で書いている。ここは次の線引きで運用する。

| | 扱い | 担保 |
|---|---|---|
| 数値・実測値 | 引用 | **必ず `ssot-assert: present-grep` を添えて正本と照合する**（`make docs/check` が html も走査する） |
| 散文（要約・主張） | 手書き | **機械判定なし。** `README.md` や `docs/**` の主張を変えたら、ここも読み直す |

カード本文まで生成物にすると HTML テンプレート機構が要り、索引生成とは別の責務が `sitegen` に混ざる。
費用対効果が合わないのでそこまではやらない、というのが現時点の判断。

```bash
make site/gen     # 文書 → data/index.json を再生成
make site/check   # 再生成して差分が出たら失敗（CI で実行）
make site/serve   # http://localhost:8000 で確認
```

## チャットの仕組み

1. ブラウザが `data/index.json` を読み、質問を BM25 で検索して上位 6 チャンクを選ぶ
2. **チャンク ID だけ**を中継サーバ（`worker/`）へ送る
3. 中継サーバが同じ索引から本文を解決し、モデルへ渡して回答をストリームで返す
4. 画面は回答と、検索でヒットした文書への出典リンクを表示する

中継先は `index.html` の `<meta name="chat-endpoint">` で指定する。**未設定・到達不能なら、
サイトは検索でヒットした抜粋の表示にフォールバックする**（チャットが落ちてもページが壊れて見えないように）。

セットアップとデプロイは [worker/README.md](worker/README.md)。

## 公開

`.github/workflows/pages.yml` が main への push で `web/`（`worker/` を除く）を GitHub Pages へ配信する。
初回だけリポジトリ設定の **Settings → Pages → Source を "GitHub Actions"** にする手動作業が要る。
