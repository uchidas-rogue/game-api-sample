// Package outbox は Transactional Outbox のドメインモデルを提供する。
package outbox

import (
	"encoding/json"
	"errors"
	"fmt"
)

// EventType はイベント種別を表す。
type EventType string

const (
	// EventTypeRankingScoreAdded は AddUserPoints 成功時に発行されるイベント種別。
	// worker は payload を RankingScoreAddedPayload として処理し、Redis に反映する。
	EventTypeRankingScoreAdded EventType = "ranking_score_added"
)

// ErrUnknownEventType は worker が未知の event_type を受け取ったときに返す。
var ErrUnknownEventType = errors.New("unknown outbox event type")

// Event は outbox から取り出した1件分のイベント。
type Event struct {
	ID         uint64
	Type       EventType
	Payload    []byte
	RetryCount uint32
}

// RankingScoreAddedPayload は EventTypeRankingScoreAdded の payload 構造。
type RankingScoreAddedPayload struct {
	UserID  int64 `json:"user_id"`
	GuildID int64 `json:"guild_id"`
	Points  int64 `json:"points"`
}

// MarshalRankingScoreAddedPayload は payload を JSON バイト列に変換する。
func MarshalRankingScoreAddedPayload(p RankingScoreAddedPayload) ([]byte, error) {
	b, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("marshal ranking score added payload: %w", err)
	}
	return b, nil
}

// UnmarshalRankingScoreAddedPayload は JSON バイト列を payload に復元する。
func UnmarshalRankingScoreAddedPayload(b []byte) (RankingScoreAddedPayload, error) {
	var p RankingScoreAddedPayload
	if err := json.Unmarshal(b, &p); err != nil {
		return RankingScoreAddedPayload{}, fmt.Errorf("unmarshal ranking score added payload: %w", err)
	}
	return p, nil
}
