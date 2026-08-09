// Package apicontract は HTTP レスポンスの「構造」を契約ファイルと突き合わせるテストヘルパー。
//
// 【なぜ構造だけを見るのか】
// 値の妥当性はハンドラやドメインのテストの責務であり、契約検証の責務ではない。
// ここが見るのは「キーの過不足」と「入れ子の形」だけ。
// これにより、契約ファイル（データ）を更新するだけでテストコードを変えずに
// 新しい契約でも検証できるようになる。逆に実装側だけがキー構造を変えるとテストが落ちる。
//
// 契約ファイルの正本は internal/driver/http/testdata/contracts/*.json。
// k6 のシナリオなど、このサービスのレスポンスを解釈する側と共有する前提のデータ。
//
// 本パッケージはテスト補助専用なので .testignore でテスト対象から除外している。
package apicontract

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
)

// AssertStructure は got（レスポンスの JSON バイト列）の構造が
// contractPath の契約ファイルと一致することを検証する。値は比較しない。
//
// got は一度 JSON として解釈し直してから比較するため、
// シリアライズできない値が紛れ込んでいる不具合も同時に検出できる。
func AssertStructure(t *testing.T, contractPath string, got []byte) {
	t.Helper()

	want, err := os.ReadFile(contractPath)
	if err != nil {
		t.Fatalf("契約ファイルを読めません: %v", err)
	}

	wantShape, err := shapeOf(want)
	if err != nil {
		t.Fatalf("契約ファイルが JSON として不正です (%s): %v", contractPath, err)
	}
	gotShape, err := shapeOf(got)
	if err != nil {
		t.Fatalf("レスポンスが JSON として不正です: %v", err)
	}

	if wantShape != gotShape {
		t.Errorf(
			"レスポンスの構造が契約と一致しません。\n"+
				"契約ファイル: %s\n"+
				"  契約: %s\n"+
				"  実際: %s\n"+
				"キー名の変更・追加・削除をしたなら、契約ファイル側も更新してください"+
				"（このレスポンスを解釈する側の変更が必要になります）。",
			contractPath, wantShape, gotShape,
		)
	}
}

// shapeOf は JSON を「型とキーだけの文字列」へ畳み込む。
//
//   - オブジェクト: キーを昇順に並べ `{k1:<shape>,k2:<shape>}`
//   - 配列: 要素の型が同じ前提で先頭要素の形を使い `[<shape>]`。空配列は `[]`
//   - スカラ: `string` / `number` / `bool` / `null`
func shapeOf(raw []byte) (string, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", err
	}
	return shape(v), nil
}

func shape(v any) string {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("%s:%s", k, shape(t[k])))
		}
		return "{" + strings.Join(parts, ",") + "}"
	case []any:
		if len(t) == 0 {
			return "[]"
		}
		// 契約は「要素の形」を1つ持てば足りる。全要素が同型である前提。
		return "[" + shape(t[0]) + "]"
	case string:
		return "string"
	case float64:
		return "number"
	case bool:
		return "bool"
	default:
		return "null"
	}
}
