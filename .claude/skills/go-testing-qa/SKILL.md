---
name: go-testing-qa
description: 本プロジェクトのテスト設計・TDD 進行・静的検証への入口。Go のテストを書く/直す/レビューする、テスト設計文書（フローチャート・テスト仕様表）を作る、カバレッジ目標を満たす、テーブル駆動テストやモックの書き方を判断する、golangci-lint のルールを追加する、といった場面で読む。
---

# Go テスト設計・品質保証

**このファイルは Claude Code 専用の索引 + Go / 本リポジトリ固有の「原則からの差分」だけ**を持つ。原則本文もプロジェクト規約本文もここには無い（同じトピックを2箇所に書くとドリフトするため）。

原則の正本は **`docs/testing/principles/` の4ファイル**にある。ここは全エージェント共通の置き場で、`.claude/` 配下ではない（AGENTS.md「指示書の読み手と置き場」）。**原則本文をこのスキルへ引き戻さないこと。**

## どこを読むか

| 問い | 正本 |
| --- | --- |
| 良いテストとは何か（テーブル駆動・モック対象・境界値・カバレッジ指標の重み付け） | [testing-principles.md](../../../docs/testing/principles/testing-principles.md) |
| どの順で書き、いつ完了とみなすか（図→テスト→実装・改善ループ・縮約の罠） | [tdd-workflow.md](../../../docs/testing/principles/tdd-workflow.md) |
| コードを実行せず構文・構造で検証するもの（静的解析・生成物整合） | [deterministic-verification.md](../../../docs/testing/principles/deterministic-verification.md) |
| 層構造固有のテスト観点（Domain / UseCase / Repository / Driver / DI） | [layered-architecture-testing.md](../../../docs/testing/principles/layered-architecture-testing.md)（§12 が層からの逆引き表） |
| Go 固有の閾値・配置・命名、およびエージェントの停止条件 | `AGENTS.md` §3・§6 |
| `docs/testing/` に何をどう書くか | [docs/testing/README.md](../../../docs/testing/README.md) |
| サブエージェントの作業手順 | `.claude/agents/test-engineer.md` |

原則側の抽象的な層名は、本リポジトリでは次のディレクトリを指す。`layered-architecture-testing.md` §12 を引くときはこの対応で読み替える。

| 原則側の層名 | 本リポジトリ |
| --- | --- |
| Domain / Entity 層 | `internal/domain/` |
| UseCase / オーケストレーション層 | `internal/usecase/` |
| Repository / Infra 層 | `internal/infrastructure/` |
| Driver / 外部インターフェース層 | `internal/driver/`（`http` / `batch` / `worker`） |
| コンポジションルート | `internal/di/` |

---

## 1. Go の言語機能が原則を代替している箇所

原則の一部は動的型付け言語を前提にしている。Go では言語機能が同じ保証を与えるため、**追加の仕組みを作らない**。

| 原則 | Go での代替 |
| --- | --- |
| ダックタイピングのテスト（インターフェイス一致の実行時エラー防止） | `var _ Iface = (*T)(nil)` + コンパイラ。AGENTS.md §2 で必須化済み。模範例: `internal/di/container.go` の実装検証ブロック |
| 未定義フィールドへの書き込み・参照の検出 | struct とコンパイラ |
| `testing-principles.md` §4「存在しない操作名をモックに指定したら即エラー」 | mockgen 生成物 + コンパイラ |
| `testing-principles.md` §4「モックのリストア保証」 | `gomock.NewController(t)` / `t.Cleanup`。グローバル書き換え型のモックは使わない |
| `deterministic-verification.md` §8 の正方向チェック（生成物の要素が実体に実在するか） | コンパイラ |
| `deterministic-verification.md` §8 の逆方向チェック（実体の変更が生成物に反映されているか） | `make gen/check`（再生成 + 差分検知）。CI 必須 |

---

## 2. テーブル駆動テストの正しい形（`testing-principles.md` §2）

**テーブルは「データ」、ランナーは「手続き」。** ケース構造体にテスト対象の生成・呼び出しを行う関数フィールドを持たせない。

```go
// NG: 各行が実行手続きを持つ「無名テストの配列」
tests := []struct {
    name  string
    setup func(t *testing.T, ctrl *gomock.Controller) (gacha.Usecase, context.Context) // ← 組み立て手順がテーブル側にある
}{...}

// OK: テーブルは入力・モックの期待値・期待結果だけを持ち、
//     対象の生成と呼び出しはランナー側に1本だけ書く
tests := []struct {
    name        string
    gemNum      int64          // 入力
    items       []domain.Item  // 入力
    repoErr     error          // モックに返させる値
    wantErr     error          // 期待結果
    checkResult func(t *testing.T, r gacha.Result) // 結果を受け取るだけの検証コールバックは可
}{...}
```

- ケース固有の追加検証が要る場合、**結果を受け取るだけの検証コールバック**（`checkErr` / `checkResult`）は §2 の許容範囲。禁止されているのは組み立て手順をテーブルに置くこと
- 共通ランナーが使う小さなヘルパー（モックの定型設定など）は**ランナー側の部品**なので関数として切り出してよい
- 手続きが違うケース・公開操作が違うケースはテーブルを分ける（§2）。ケースが1件でもテーブル形式を保つ

---

## 3. モック対象の Go 固有判断（`testing-principles.md` §3）

配置・生成コマンド・バージョン固定の規約は AGENTS.md §3 が正本。ここは「何をモックするか」だけ。

- **モックするのは `internal/usecase/` が定義する interface のみ**（Repository / Transactor / RankingStore / Notifier / Randomizer 等）
- **`internal/domain/` の entity・値・sentinel error・定数はモックしない**。外部依存を持たないので実物を使う（`layered-architecture-testing.md` §1）
- 生成物の配置は `<interface と同じパッケージ>/mock/`。**`internal/testutil/mock/` ではない**（`internal/testutil/` はフィクスチャ・ヘルパー置き場で、モックとは別）

---

## 4. カバレッジ指標 — Go の制約と代替（重要）

`testing-principles.md` §11 は**行/文カバレッジと分岐カバレッジの両方を必須指標**とするが、**Go の `go test -cover` は文（statement）カバレッジしか出力せず、成熟した分岐カバレッジ計測ツールも存在しない**。

そのため本プロジェクトは以下の割り当てで §11 を満たす。**これは原則からの意図的な逸脱であり、その理由を明示するためにここに記す。**

| §11 の指標 | 本プロジェクトでの担保方法 |
| --- | --- |
| 行/文カバレッジ | 層別閾値の **CI 機械判定**（`make test/cover/check`。閾値は AGENTS.md §3、実行可能な写しが `scripts/coverage-check.sh`）。未達なら CI が落ちる |
| 分岐カバレッジ | **代替: フロー図のパスカバレッジ**。[docs/testing/](../../../docs/testing/) の設計図とテスト仕様表を正本とし、表とテストコードの対応（ケース順・件数・終端ノードの網羅）は `scripts/doccheck` が **CI 機械判定**、残りは [docs/testing/README.md](../../../docs/testing/README.md) §6 のチェックリストで**レビューゲート**として担保する |
| 条件カバレッジ・関数カバレッジ | 参考値（§11 のとおり目標から除外） |

つまり **分岐の網羅は数値ではなく設計文書とレビューで守る**。フロー図を作る対象（`internal/usecase/` / `internal/driver/`）でこれを省略すると、分岐カバレッジの担保手段が完全に失われる。

**`go tool cover -func` の数値で判断しないこと。** あれは関数単位の平均で、小さな未カバー関数を過大に、大きな高カバレッジ関数を過小に評価する。`make test/cover/check` は文数で重み付けした集計（`go tool cover -func` の `total` と一致する定義）を使う。

未達だったときの進め方（分類 → 到達可能なものだけ追加 → 過剰モック禁止 → 試行上限で打ち切り報告）は **AGENTS.md §6 が正本**。一般原則は `tdd-workflow.md` §11。ここには書かない。

`testing-principles.md` §12 の「テスト対象外を明示する」は、本リポジトリでは **`.testignore`** が実装になっている（運用は AGENTS.md §5）。

---

## 5. 層別の適用メモ

原則を本リポジトリの実装に当てはめる際、**原則の記述だけでは判断がつかない箇所**のみを挙げる。層ごとの一般的な観点は `layered-architecture-testing.md` §12 を引く。

### `internal/domain/`

- `layered-architecture-testing.md` §3 の「mutation メソッドで不変条件を守る」は、Go では**非公開フィールド + 業務メソッド**で表現する。生の setter を公開しない
- 境界値は `testing-principles.md` §7 の3基準で取捨する。機械的に 0・負数・上限を並べない

### `internal/usecase/`

- トランザクション境界（`shared.Transactor`）は**モックしてよい**。境界自身の契約は `internal/infrastructure/mysql/transactor_test.go` でのみ検証する。必須シナリオの一覧は [docs/testing/transaction-boundary.md](../../../docs/testing/transaction-boundary.md)

### `internal/infrastructure/`

- `layered-architecture-testing.md` §5 の CRUD 必須ケース表のうち、**Identity Map 系のケースは該当しない**。本リポジトリの repository は sqlc を直接呼び、同一トランザクション内のインスタンス追跡を行っていないため
- 同 §6 の「**クエリ構築引数の検証**」（`FOR UPDATE` を取っているか、絞り込み条件が正しいか）は基底テストで代替できない。ここを省略しない
- 同 §8「テスト用シームを本番コードから使わせない」は、`internal/infrastructure/mysql/repository/export_test.go` の `NewXxxRepositoryWithQuerier` が `_test.go` にあり本番ビルドから不可視である点で**既に満たしている**。この形を崩さない

### 横断（`internal/di/`・共有状態）

- `layered-architecture-testing.md` §8「生成箇所をコンポジションルートに集約」は、`.golangci.yml` の `depguard` が「`internal/infrastructure` を import できるのは `internal/di` と `cmd` のみ」というルールで静的に強制している
- 同 §10「パッケージスコープの可変変数を作らない」は `gochecknoglobals` が検出する。ただし**テストコードは対象外**にしている（宣言は見えるが「書換」を判別できず、読み取り専用フィクスチャまで落ちるため）。`t.Parallel()` 配下の pkg-global 書換禁止（AGENTS.md §3）は引き続き人手で守る

---

## 6. 静的検証を足すときの設計原則

原則は `deterministic-verification.md`。**判定の実体と、採用理由・除外理由・見送った linter の理由はすべて `.golangci.yml` のコメントに書いてある。判定を変えるときは、まずそのコメントを読む。**

Go 固有の適用メモは以下の3点のみ。

- 引数の内容まで条件に含める規約（例: `slog.String("error", ...)` だけを禁止し `slog.String("request_id", ...)` は許す）は `forbidigo` では表現できないため、`scripts/ruleguard/rules.go` に AST パターンとして書く（§3「構文解析ベースで判定する」）
- `forbidigo` は正規表現ベースなので `analyze-types: true` を有効化し、既知の誤検出パターンと回避策を `.golangci.yml` のコメントに残す（§3）
- 規約を機械検証へ移したら、`.claude/hooks/remind-docs.sh` の変更検知パターンにその設定ファイルを追加する（規約の正本が増えるため）
