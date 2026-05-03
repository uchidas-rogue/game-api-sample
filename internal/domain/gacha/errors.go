package gacha

import "errors"

// ガチャドメインのビジネスエラー。各層は errors.Is でこれらを判定する。
var (
	// ErrInsufficientGems は石不足を表す。
	ErrInsufficientGems = errors.New("insufficient gems for multi pull")
	// ErrNoItemsAvailable は抽選対象のアイテムマスタが空であることを表す。
	ErrNoItemsAvailable = errors.New("no items available for gacha")
	// ErrInvalidItemWeights はアイテム重みの合計値が0以下であることを表す。
	ErrInvalidItemWeights = errors.New("invalid item weights: total must be positive")
	// ErrInvalidPullCount は pullCount が許容範囲外であることを表す。
	ErrInvalidPullCount = errors.New("invalid pull count")
	// ErrUserNotFound は対象ユーザーが存在しないことを表す。
	// infrastructure 層が sql.ErrNoRows 等の DB 固有エラーを本エラーに変換する。
	ErrUserNotFound = errors.New("user not found")
)
