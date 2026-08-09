---
description: DB スキーマを追加する（migration → schema.sql → query → sqlc → repository → DI）
argument-hint: <テーブル名と目的（例: user_stamina スタミナ管理）>
---

DB スキーマ「$ARGUMENTS」を追加する。AGENTS.md §4 の手順を守る。**順序を入れ替えない。**

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

`deployments/mysql/schema.sql` は生成物。**手動編集しない**。

## 3. クエリを追加

`deployments/mysql/queries/<機能>.sql` に `-- name: XxxYyy :one|:many|:exec` 形式で書く。

- 更新目的で行を読むなら `FOR UPDATE` を明示する
- 複数行にロックを取るなら取得順序を決定的にする（ID 昇順等）。デッドロック回避の不変条件になる

## 4. sqlc でコード生成

```
make db/sqlc/gen
```

`sqlc generate` を直接叩かない（PATH 上の別バージョンで生成結果が変わる）。
バージョンは `.sqlc-version` で固定されている。

## 5. マイグレーション適用

```
make db/migrate/up
```

## 6. repository 実装

`internal/infrastructure/mysql/repository/` に実装する。

- interface は **`usecase` 層に定義する**（`infrastructure` ではない）
- 実装型の定義直前に `var _ <Iface> = (*<Type>)(nil)` を書く
- sqlc 型 ⇄ domain 型の変換は infrastructure 層で行う
- `sql.ErrNoRows` 等の DB 固有エラーは domain のエラー（`ErrNotFound` 等）へ変換する
- interface を新設したら `//go:generate mockgen` を interface 定義ファイルに書き `make mock/gen`

## 7. DI 登録

`internal/di/container.go` に配線し、同ファイル冒頭の
`var ( _ <Iface> = (*<Impl>)(nil) ... )` ブロックにも追加する。

## 8. テスト

repository のテストは実リソースを起動しない。
`export_test.go` のテスト専用コンストラクタ経由で `sqlc.Querier` のモックを注入する。

検証対象は次の4つ。

1. sqlc 型 ⇄ domain 型の変換
2. エラー変換（`sql.ErrNoRows` → domain エラー）
3. **クエリ構築引数**（`FOR UPDATE` の有無、絞り込み条件）— 基底のテストでは代替できない
4. レコードあり / なし の両方

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
  `config_test.go` の `clearConfigEnv` を更新する
- スキーマ構成が ROADMAP や `terraform/ARCHITECTURE.md` の記述と矛盾しないか確認する

commit / push はユーザーの明示指示があるまで行わない。
