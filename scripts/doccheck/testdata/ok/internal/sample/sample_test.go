// Package sample_test は doccheck のフィクスチャ。ビルド対象外（testdata 配下）。
package sample_test

import "testing"

// TestSample はテーブル駆動のケースにマーカーを置いた形。
func TestSample(t *testing.T) {
	tests := []struct{ name string }{
		{
			// #1 A→B→E1
			name: "入力が不正",
		},
		{
			// #2 …→C→Z
			// 補足のコメントがマーカーの後ろに続いても、グループ先頭がマーカーなら読み取れる。
			name: "正常系",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(_ *testing.T) {})
	}
}

// TestSampleContract は1関数=1ケースの形（テーブルを持たない）。
func TestSampleContract(t *testing.T) {
	// #3
	_ = t
}

// TestSampleOneMarkerTwoCases は「1マーカーが複数の要素を覆う」形。
// 表の1行に対してテストケースを2つ展開しても、マーカーが1つなら1ケースとして数える。
func TestSampleOneMarkerTwoCases(t *testing.T) {
	tests := []struct{ name string }{
		{
			// #1 A→B→E1
			name: "同じ構造・入力違い その1",
		},
		{
			name: "同じ構造・入力違い その2",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(_ *testing.T) {})
	}
}

// TestSampleFuncsMode は testcases-funcs アンカーから名前で参照される関数。
func TestSampleFuncsMode(t *testing.T) {
	t.Run("サブテスト名", func(_ *testing.T) {})
}
