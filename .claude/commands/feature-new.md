---
description: 新機能を「設計図 → 失敗するテスト → 実装」の順で追加する。API エンドポイント・ユースケース・バッチ/ワーカー処理を新設する、既存の usecase / driver に分岐や処理ノードを足す、といった場面で使う。実装を先に書いてから図とテストを後付けするのを防ぐための手順書。
argument-hint: <機能名と概要（例: ユーザーのスタミナ回復API）>
---

新機能「$ARGUMENTS」を追加する。**この順序を崩さない。**
コードを先に書いてから図とテストを後付けしない。

## 0. 前提の確認

- [docs/testing/README.md](../../docs/testing/README.md) を読む（図の使い分け・仕様表の形・チェックリストの正本）
- 対象層のテスト観点を [docs/testing/principles/layered-architecture-testing.md](../../docs/testing/principles/layered-architecture-testing.md) **§12 の逆引き表**から引く
- 新規パッケージ追加・ディレクトリ構造変更を伴うなら、着手前にユーザーへ確認する（AGENTS.md §6）

## 1. 設計図とテスト仕様表（実装より先）

`usecase` / `driver` に手を入れるなら `docs/testing/<機能>.md` を作る。
**作り方・仕様表の列・並び順・重複排除のルールは
[docs/testing/README.md](../../docs/testing/README.md) §1〜§3 に従う**（ここには写さない）。

フローチャートでは**すべての終端ノード**（正常終了・各エラー）に ID を振ること。
テスト仕様表の「図のパス」列がその ID を参照する。

## 2. 失敗するテスト（RED）

依存の少ない層から書く: `domain` → `usecase`（モック）→ `infrastructure` → `driver`。

- テーブルは**データのみ**を持つ。テスト対象の生成やモックの組み立てをケース構造体の
  関数フィールドに入れない。組み立てはランナー1本に集約する（`go-testing-qa` スキル §2）
- モックするのは `usecase` が定義した interface だけ。`domain` の値・sentinel error は実物を使う
- interface を新設・変更したら `make mock/gen`（再生成要否の判断は不要。AGENTS.md §3）

この時点でテストが**失敗すること**を確認してから次へ進む。

## 3. 実装（GREEN）

テストを通す最小限の実装を書く。AGENTS.md §1・§2 の規約に従う。

- 層をまたぐ import は `.golangci.yml` の `depguard` が判定する。新しい外部ライブラリを使うなら、
  その層の `allow` に**理由付きで**追加する。`//nolint` で個別に抜け道を作らない
- 新しい設定値を足したら `configs/config.go` の3箇所と `config_test.go` の `clearConfigEnv` を更新する
- infrastructure の実装を追加したら `internal/di/container.go` に DI 登録する

## 4. 検証

以下を順に実行し、すべて通ること（実行条件の詳細は AGENTS.md §6）。

```
make lint
make gen/check
make test
make test/race
make test/cover/check
```

`deployments/mysql/**` か `sqlc.yaml` を触ったなら `make db/gen/check` も。

## 5. 仕上げの突き合わせ

- [docs/testing/README.md](../../docs/testing/README.md) §6 のレビューゲート用チェックリスト（6項目）を回す
- カバレッジ閾値が未達なら **AGENTS.md §6「カバレッジ未達時の改善ループ」**に従う

commit / push はユーザーの明示指示があるまで行わない。
