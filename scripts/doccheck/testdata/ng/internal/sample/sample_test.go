// Package sample_test は doccheck のフィクスチャ。ビルド対象外（testdata 配下）。
// 意図的に「表と食い違った」状態を作ってある。
package sample_test

import "testing"

// TestRowCount は表より1件少ないケースしか持たない。
func TestRowCount(t *testing.T) {
	tests := []struct{ name string }{
		{
			// #1 A→B→E1
			name: "入力が不正",
		},
		{
			// #2 …→C→Z
			name: "正常系",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(_ *testing.T) {})
	}
}

// TestOrder はケースの並びが表と入れ替わっている。
func TestOrder(t *testing.T) {
	tests := []struct{ name string }{
		{
			// #2 …→C→Z
			name: "正常系",
		},
		{
			// #1 A→B→E1
			name: "入力が不正",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(_ *testing.T) {})
	}
}

// TestPath はマーカーのパスが表と違う（図の変更に片方だけ追随した状態）。
func TestPath(t *testing.T) {
	tests := []struct{ name string }{
		{
			// #1 A→B→C→Z
			name: "入力が不正",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(_ *testing.T) {})
	}
}

// TestEdge は表と一致しているが、そのパスが図に無い辺を通っている。
func TestEdge(t *testing.T) {
	tests := []struct{ name string }{
		{
			// #1 A→Z
			name: "図に無い辺",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(_ *testing.T) {})
	}
}

// TestTerminal は図の終端のうち1つしか通さない。
func TestTerminal(t *testing.T) {
	tests := []struct{ name string }{
		{
			// #1 A→B→E1
			name: "エラー側だけ",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(_ *testing.T) {})
	}
}

// TestOrphan はどの仕様表からも参照されていないマーカーを持つ。
func TestOrphan(t *testing.T) {
	tests := []struct{ name string }{
		{
			// #1 A→B→E1
			name: "表から参照されていない",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(_ *testing.T) {})
	}
}
