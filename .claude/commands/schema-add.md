---
description: DB スキーマを追加する（migration → schema.sql → query → sqlc → repository → DI）。テーブル・カラム・インデックスを新設または変更する、新しい SQL クエリを足す、repository を実装する、といった場面で使う。生成物の再生成順序を飛ばして sqlc / schema.sql が食い違うのを防ぐための手順書。
argument-hint: <テーブル名と目的（例: user_stamina スタミナ管理）>
---

DB スキーマ「$ARGUMENTS」を追加する。**AGENTS.md §4 が規約の正本。本ファイルは実行順序だけを定める。順序を入れ替えない。**

## 0. 前提の確認

- `down` が困難な変更（カラム削除等）を含むなら、**着手前にユーザーへ確認する**（AGENTS.md §4 / §6）
- MySQL コンテナが起動していること: `docker compose -f deployments/docker-compose.yml up -d mysql`

## 1. マイグレーション作成

```
make db/migrate/new name=<snake_case_name>
```

`deployments/mysql/migrations/` に生成された `up` / `down` の**両方**を書く。
`down` を空のまま放置しない。

## 2. schema.sql を再生成

```
make db/schema/dump
```

## 3. クエリを追加

`deployments/mysql/queries/<機能>.sql` に `-- name: XxxYyy :one|:many|:exec` 形式で書く。

- 更新目的で行を読むなら `FOR UPDATE` を明示する
- 複数行にロックを取るなら取得順序を決定的にする（ID 昇順等）。デッドロック回避の不変条件になる

## 4. sqlc でコード生成

```
make db/sqlc/gen
```

## 5. マイグレーション適用

```
make db/migrate/up
```

## 6. repository 実装

`internal/infrastructure/mysql/repository/` に実装する。規約は AGENTS.md §1・§2・§4
（interface は `usecase` 層に定義 / `var _ Iface = (*Type)(nil)` / sqlc 型 ⇄ domain 型の変換 /
`sql.ErrNoRows` → domain エラーへの変換）。

interface を新設したら `//go:generate mockgen` を interface 定義ファイルに書き `make mock/gen`。

## 7. DI 登録

`internal/di/container.go` に配線し、同ファイル冒頭の
`var ( _ <Iface> = (*<Impl>)(nil) ... )` ブロックにも追加する。

## 8. テスト

方針（実リソースを起動しない・`export_test.go` 経由で `sqlc.Querier` モックを注入）と
検証対象は **AGENTS.md §3 の `infrastructure` 層の項**が正本。

このコマンド固有の注意は1点のみ:
**クエリ構築引数**（`FOR UPDATE` の有無・絞り込み条件）の検証を省略しない。
ここは他のどのテストでも代替できない（`go-testing-qa` スキル §5）。

## 9. 検証

```
make lint
make db/gen/check
make gen/check
make test
make test/race
make test/cover/check
```

## 10. 資料の更新

- 新しい設定値（接続プール等）を足したなら `configs/config.go` の3箇所と
  `config_test.go` の `clearConfigEnv` を更新する（AGENTS.md §2）
- スキーマ構成が ROADMAP や `terraform/ARCHITECTURE.md` の記述と矛盾しないか確認する

commit / push はユーザーの明示指示があるまで行わない。
