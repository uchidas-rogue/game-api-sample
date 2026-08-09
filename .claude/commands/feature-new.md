---
description: 新機能を「設計図 → 失敗するテスト → 実装」の順で追加する
argument-hint: <機能名と概要（例: ユーザーのスタミナ回復API）>
---

新機能「$ARGUMENTS」を追加する。**この順序を崩さない。**
コードを先に書いてから図とテストを後付けしない。

## 0. 前提の確認

- `go-testing-qa` スキルの `references/tdd-workflow.md` と `references/layered-architecture-testing.md` を読む
- [docs/testing/README.md](../../docs/testing/README.md) で図の使い分けを確認する
- 新規パッケージ追加・ディレクトリ構造変更を伴うなら、着手前にユーザーへ確認する（AGENTS.md §6）

## 1. 設計図とテスト仕様表（実装より先）

`usecase` / `driver` に手を入れるなら `docs/testing/<機能>.md` を作る。

- フローチャート（mermaid）を描き、**すべての終端ノード**（正常終了・各エラー）に ID を振る
- テスト仕様表を作る。列は `# | 条件 | 図のパス | 期待結果 | 検証すべき呼び出し`
- 並び順は**図のパスが短い順**。同一パスのケースは統合する
- **契約表・決定表は作らない**

`domain` の純粋関数・値オブジェクトは図を作らない。テストケース配列自体が正本になる。

## 2. 失敗するテスト（RED）

依存の少ない層から書く: `domain` → `usecase`（モック）→ `infrastructure` → `driver`。

- テーブルは**データのみ**を持つ。テスト対象の生成やモックの組み立てをケース構造体の
  関数フィールドに入れない。組み立てはランナー1本に集約する
- モックするのは `usecase` が定義した interface だけ。`domain` の値・sentinel error は実物を使う
- interface を新設・変更したら `make mock/gen`

この時点でテストが**失敗すること**を確認してから次へ進む。

## 3. 実装（GREEN）

テストを通す最小限の実装を書く。AGENTS.md §1・§2 の規約に従う。

- 層をまたぐ import は `.golangci.yml` の `depguard` が判定する。新しい外部ライブラリを使うなら、
  その層の `allow` に**理由付きで**追加する。`//nolint` で個別に抜け道を作らない
- 新しい設定値を足したら `configs/config.go` の3箇所と `config_test.go` の `clearConfigEnv` を更新する
- infrastructure の実装を追加したら `internal/di/container.go` に DI 登録する

## 4. 検証

以下を順に実行し、すべて通ること。

```
make lint
make gen/check
make test
make test/race
make test/cover/check
```

`deployments/mysql/**` か `sqlc.yaml` を触ったなら `make db/gen/check` も。

## 5. 仕上げの突き合わせ

[docs/testing/README.md](../../docs/testing/README.md) §6 のチェックリストを回す。

- [ ] テスト仕様表のケース番号とテストコードのケース順が一致している
- [ ] 図中のすべての終端ノードに対応するケースが最低1件ある
- [ ] 同一パスの重複ケースが排除されている
- [ ] ケースが「パスが短い順」に並んでいる

カバレッジが閾値未達なら、未カバー箇所を
「通常の分岐 / エントリポイント / デッドコード / 外部要因依存」に分類し、
**到達可能なものだけ**テストを足す。未カバー行を通すためだけの不自然なモックは作らない。
試行が続くようなら打ち切り、内訳（行・分類・理由）を報告する。

commit / push はユーザーの明示指示があるまで行わない。
