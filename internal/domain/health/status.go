// Package health はヘルスチェックに関するドメインモデルを定義する。
package health

// HealthStatus はサービスの稼働状態を表す値オブジェクト。
type HealthStatus string

// 稼働状態を表す定数群（マジックナンバー禁止ルール対応）。
const (
	// StatusOK はサービスが正常稼働している状態。
	StatusOK HealthStatus = "ok"
	// StatusDegraded は一部依存リソースに問題がある状態（将来用）。
	StatusDegraded HealthStatus = "degraded"
	// StatusDown はサービスが停止している状態（将来用）。
	StatusDown HealthStatus = "down"
)

// String はHealthStatusの文字列表現を返す。
func (s HealthStatus) String() string {
	return string(s)
}
