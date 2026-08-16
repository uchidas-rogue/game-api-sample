// Package sample_test は doccheck のフィクスチャ。ビルド対象外（testdata 配下）。
package sample_test

import "testing"

// TestDup は同じ辺が2度書かれた図に対応するケース。
func TestDup(t *testing.T) {
	tests := []struct{ name string }{
		{
			// #1 A→B
			name: "理由1 で後始末へ",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(_ *testing.T) {})
	}
}
