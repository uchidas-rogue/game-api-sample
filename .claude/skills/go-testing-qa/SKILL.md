---
name: go-testing-qa
description: 本プロジェクトのテスト設計・TDD 進行・静的検証の正本。Go のテストを書く/直す/レビューする、テスト設計文書（フローチャート・テスト仕様表）を作る、カバレッジ目標を満たす、テーブル駆動テストやモックの書き方を判断する、golangci-lint のルールを追加する、といった場面で読む。
---

# Go テスト設計・品質保証

テスト設計の一般原則は `references/` の4ファイルが正本。**このファイルは Go / 本リポジトリ固有の適用ノートだけ**を持つ。原則本文をここに再掲しない（4ファイル自身の「同じトピックを複数ファイルに書くとドリフトする」という原則に従う）。

## 参照ファイル

| ファイル | 扱う問い |
| --- | --- |
| `references/testing-principles.md` | 良いテストとは何か（実行して振る舞いを検証する側の一般原則） |
| `references/tdd-workflow.md` | どの順で書き、いつ完了とみなすか |
| `references/deterministic-verification.md` | コードを実行せず構文・構造で検証するもの（静的解析・生成物整合） |
| `references/layered-architecture-testing.md` | Domain/UseCase/Repository 等、層構造固有のテスト観点 |

規約の役割分担:

- **テスト設計の考え方 → 本スキル（references/）**
- **Go 固有の閾値・配置・命名 → `AGENTS.md` §3**
- **サブエージェントの作業手順 → `.claude/agents/test-engineer.md`**

---

## 1. 層の対応

原則側の抽象的な層名を、本リポジトリのディレクトリへ読み替える。

| 原則側の層名 | 本リポジトリ |
| --- | --- |
| Domain / Entity 層 | `internal/domain/` |
| UseCase / オーケストレーション層 | `internal/usecase/` |
| Repository / Infra 層 | `internal/infrastructure/` |
| Driver / 外部インターフェース層 | `internal/driver/`（`http` / `batch` / `worker`） |
| コンポジションルート | `internal/di/` |

`layered-architecture-testing.md` §12 の層別クイックリファレンスを引くときは、この対応表で読み替える。

---

## 2. Go の言語機能が原則を代替している箇所

原則の一部は動的型付け言語を前提にしている。Go では言語機能が同じ保証を与えるため、**追加の仕組みを作らない**。

| 原則 | Go での代替 |
| --- | --- |
| ダックタイピングのテスト（インターフェイス一致の実行時エラー防止） | `var _ Iface = (*T)(nil)` + コンパイラ。AGENTS.md §2 で必須化済み。模範例: `internal/di/container.go` の実装検証ブロック（コンポジションルートで全インターフェースをまとめて検証している） |
| 未定義フィールドへの書き込み・参照の検出 | struct とコンパイラ |
| `testing-principles.md` §4「存在しない操作名をモックに指定したら即エラー」 | mockgen 生成物 + コンパイラ |
| `deterministic-verification.md` §8 の正方向チェック（生成物の要素が実体に実在するか） | コンパイラ |
| `deterministic-verification.md` §8 の逆方向チェック（実体の変更が生成物に反映されているか） | `make gen/check`（再生成 + 差分検知）。CI 必須。再生成忘れ・生成物の手動編集の両方を検知する |

---

## 3. テーブル駆動テストの正しい形（`testing-principles.md` §2）

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

## 4. モックの扱い（`testing-principles.md` §3・§4）

- **モックするのは `internal/usecase/` が定義する interface のみ**（Repository / Transactor / RankingStore / Notifier / Randomizer 等）
- **`internal/domain/` の entity・値・sentinel error・定数はモックしない**。外部依存を持たないので実物を使う（`layered-architecture-testing.md` §1）
- 手書きモック禁止。`//go:generate mockgen` を interface 定義ファイルに書き、`make mock/gen` で生成する（`go generate ./...` を直接叩かない。PATH 上の別バージョンの mockgen が使われ、生成結果が変わる）
- 生成物を手で編集しない。編集しても `make gen/check` が再生成して差分として検知する
- 生成物の配置は `<interface と同じパッケージ>/mock/`（例: `internal/usecase/gacha/mock/`）。**`internal/testutil/mock/` ではない**
- テスト用のフィクスチャ・ヘルパーは `internal/testutil/` 配下に置く（モックとは別）
- Go では `t.Cleanup` / `gomock.NewController(t)` が §4 の「リストア保証」を満たす。グローバル書き換え型のモックは使わない

---

## 5. カバレッジ指標 — Go の制約と代替（重要）

`testing-principles.md` §11 は**行/文カバレッジと分岐カバレッジの両方を必須指標**とするが、**Go の `go test -cover` は文（statement）カバレッジしか出力せず、成熟した分岐カバレッジ計測ツールも存在しない**。

そのため本プロジェクトは以下の割り当てで §11 を満たす。**これは原則からの意図的な逸脱であり、その理由を明示するためにここに記す。**

| §11 の指標 | 本プロジェクトでの担保方法 |
| --- | --- |
| 行/文カバレッジ | 層別閾値の CI 判定（層別目標は AGENTS.md §3）。**閾値判定は未実装** — 現状 CI は `go tool cover -func` の結果をサマリ表示するだけで、未達でも落ちない |
| 分岐カバレッジ | **代替: `tdd-workflow.md` §7 のフロー図パスカバレッジ**。テスト仕様表「図のパス」列の網羅を、同 §8 チェックリストによる**レビューゲート**で担保する。**設計文書の置き場（`docs/testing/`）は未整備** |
| 条件カバレッジ・関数カバレッジ | 参考値（§11 のとおり目標から除外） |

つまり **分岐の網羅は数値ではなく設計文書とレビューで守る**。フロー図を作る対象（`internal/usecase/` / `internal/driver/`）でこれを省略すると、分岐カバレッジの担保手段が完全に失われる。

現状のカバレッジ確認手段は `make test/cover`（HTML 表示）と `make test/race`（`coverage.out` 生成）。

---

## 6. テスト対象外の宣言（`testing-principles.md` §12）

§12 の「テスト対象外を明示し、なぜ対象外かをコメントに残す」は、本リポジトリでは **`.testignore`** が実装になっている。

- 各ブロックに「除外理由」と「解除条件」をコメントで書く運用が既にある
- 新たに除外したいパッケージが出たら、理由を書いたうえで追記する
- 実装が増えて除外を解除する場合は行を削除し、AGENTS.md §3 のカバレッジ目標に従う

---

## 7. 改善ループの試行上限（`tdd-workflow.md` §11）

§11 の「カバレッジ改善ループには必ず試行回数の上限を設ける」は、本リポジトリの既存規約と**同じ思想**。重複して別の上限を定義せず、以下を参照する。

- `AGENTS.md` §6「同一のテスト失敗・コンパイルエラーが3回連続で再発した場合はユーザーに方針を確認する」
- `.claude/agents/test-engineer.md` §6「同一のテスト失敗・コンパイルエラーが 3 回連続で再発した場合は自律修正を中断」

§11 が追加で要求するのは以下。**カバレッジ閾値が CI 強制されている以上、未カバー行を無理やり通すテストを書く誘因があるため、この歯止めは必須**。

- 未カバー箇所を「通常の分岐（到達可能）/ エントリポイント / デッドコード / 外部要因依存」に分類し、**到達可能なものだけ**テストを追加する
- **過剰モック禁止**: 未カバー行を実行させるためだけの不自然なモック・テストケースを追加しない
- 上限に達しても未達なら打ち切り、未カバー箇所の内訳（行・分類・理由）を報告して終了する
- リファクタ・テスト縮約の後は**必ずカバレッジを再計測**する（§12）。低下したら黙って戻さず、縮約方針の見直しを含めて報告する

---

## 8. 層別の適用メモ

原則を本リポジトリの実装に当てはめる際の注意。

### `internal/domain/`

- `layered-architecture-testing.md` §3 の「mutation メソッドで不変条件を守る」は、Go では**非公開フィールド + 業務メソッド**で表現する。生の setter を公開しない
- 境界値は §7 の3基準（通る分岐が違う / 出力が違う / 片方だけが検出できる実装ミスがある）で取捨する。機械的に 0・負数・上限を並べない
- **フロー図は作らない**。`tdd-workflow.md` §3 のとおり、テストケース配列自体を SSoT とし、各ケースに意図の注記を添える

### `internal/usecase/`

- トランザクション境界は `shared.Transactor` を**モックしてよい**。境界自身の契約は `internal/infrastructure/mysql/transactor_test.go` でのみ検証する（`layered-architecture-testing.md` §7）
- 下位層（domain）が担保済みの境界値バリエーションを重ねてテストしない（`testing-principles.md` §5）。この層が検証するのは「domain の判定結果を受けた分岐」
- 複数行に同時にロックを取る処理は、取得順序の決定性（デッドロック回避の不変条件）をテストで確認する（§7 末尾）
- **フローチャート + テスト仕様表を作る**（置き場は `docs/testing/`。**未整備**）

### `internal/infrastructure/`

- `layered-architecture-testing.md` §5 の CRUD 必須ケース表のうち、**Identity Map 系のケースは該当しない**。本リポジトリの repository は sqlc を直接呼び、同一トランザクション内のインスタンス追跡を行っていないため
- 適用するのは「レコードあり → domain 型に変換」「レコードなし → `sql.ErrNoRows` を domain のエラー（`ErrNotFound` 等）へ変換」「**クエリ構築引数の検証**（`FOR UPDATE` を取っているか、絞り込み条件が正しいか。§6 のとおりここは基底テストで代替できない）」
- トランザクション境界の契約は §7 の4シナリオ（境界内失敗→ロールバックして元の原因を再送出 / ロールバック自体の失敗でも元の原因を返す / コミット失敗の伝播 / 空の結果で成功）を `transactor_test.go` で担保する
- §8「テスト用シームを本番コードから使わせない」は、`internal/infrastructure/mysql/repository/export_test.go` の `NewXxxRepositoryWithQuerier` が `_test.go` にあり本番ビルドから不可視である点で**既に満たしている**。この形を崩さない

### `internal/driver/`

- 変換層の責務に限定する（`testing-principles.md` §9）。下位層の判定結果を再テストしない
- 透過マッピングは正常系1件で構造を担保すれば十分。値違いのケースを増やさない
- 失敗経路が複数段階ある場合、テスト仕様表に「**検証すべき呼び出し**」列を足し、各パスで呼ばれるべき下位層の呼び出し（引数・回数）まで確認する（`tdd-workflow.md` §4・§7）
- **フローチャート + テスト仕様表を作る**（置き場は `docs/testing/`。**未整備**）

### 横断（`internal/di/`・共有状態）

- 生成箇所は `internal/di/container.go` に一元化する（`layered-architecture-testing.md` §8）。`.golangci.yml` の `depguard` が「`internal/infrastructure` を import できるのは `internal/di` と `cmd` のみ」というルールで静的に強制している
- パッケージスコープの可変変数を作らない（§10）。許されるのは `const`、`var _ Iface = (*T)(nil)`、domain の sentinel error のみ。`gochecknoglobals` が検出する
  - ただし**テストコードは対象外**にしている（宣言は見えるが「書換」を判別できず、読み取り専用フィクスチャまで落ちるため）。`t.Parallel()` 配下の pkg-global 書換禁止（AGENTS.md §3）は引き続き人手で守る

---

## 9. 静的検証を足すときの設計原則

`deterministic-verification.md` に沿う。新しい lint ルールを追加する際に守ること。

判定の実体は `.golangci.yml`（`make lint`）。採用理由・除外理由・見送った linter の理由はすべて同ファイルにコメントで残してある。**判定を変えるときは、まずそのコメントを読む。**

引数の内容まで条件に含める規約（例: `slog.String("error", ...)` だけを禁止し `slog.String("request_id", ...)` は許す）は forbidigo では表現できないため、`scripts/ruleguard/rules.go` に AST パターンとして書く。

- **レビューで毎回同じ指摘をしている規約は機械検証へ回す**（§1）
- **可能な限り AST ベースで判定する**（§3）。`depguard`（import パス）・`gochecknoglobals`（AST）が該当。`forbidigo` は正規表現ベースなので `analyze-types: true` を有効化し、既知の誤検出パターンと回避策を `.golangci.yml` のコメントに残す
- **ホワイトリスト方式**（§4）。「原則禁止、許可されたものだけ例外」で書き、各許可項目に理由をコメントで対応づける
- **規則に当てはまらないケースが出たら、その場で個別の例外（`//nolint`）を作らず、まず規則セット自体を拡張する**（§2）
- **差分駆動 + SSoT 変更時は全件へフォールバック**（§6）。lint 設定ファイル自体が変わった PR は全件スキャンする
- **ローカルと CI で同一のチェッカーを同一引数で実行できるようにする**（§7）。`make lint` が CI と同じコマンドを呼ぶ
- 規約を機械検証へ移したら、`.claude/hooks/remind-docs.sh` の変更検知パターンにその設定ファイルを追加する（規約の SSoT が増えるため）
