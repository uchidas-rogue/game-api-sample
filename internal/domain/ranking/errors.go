package ranking

import "errors"

var (
	// ErrGuildNotFound は対象ギルドが存在しないことを表す。
	ErrGuildNotFound = errors.New("guild not found")
	// ErrUserNotFound は対象ユーザーが存在しないことを表す。
	ErrUserNotFound = errors.New("user not found")
	// ErrInvalidScore はスコアが無効であることを表す。
	ErrInvalidScore = errors.New("invalid score")
	// ErrInvalidPoints はポイントが無効であることを表す。
	ErrInvalidPoints = errors.New("invalid points")
	// ErrUserNotInGuild はユーザーがギルドに所属していないことを表す。
	ErrUserNotInGuild = errors.New("user is not a member of the guild")
	// ErrScoreNotFound はギルドのスコアが未登録であることを表す。
	ErrScoreNotFound = errors.New("score not found for guild")
	// ErrPointsNotFound はユーザーのポイントが未登録であることを表す。
	ErrPointsNotFound = errors.New("points not found for user")
	// ErrRankingUnavailable はランキングが未構築（Redis 揮発を含む）であることを表す。
	// 「対象が未登録」を表す ErrScoreNotFound / ErrPointsNotFound とは区別する。
	// あちらは 404 だが、こちらは再構築すれば解消する一時的な状態なので 503 に対応する。
	ErrRankingUnavailable = errors.New("ranking is unavailable")
)
