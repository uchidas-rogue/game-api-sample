package ranking

//go:generate mockgen -source=usecase.go -destination=mock/mock_usecase.go -package=mock_ranking

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	outboxdomain "github.com/uchidas-rogue/game-api-sample/internal/domain/outbox"
	rankingdomain "github.com/uchidas-rogue/game-api-sample/internal/domain/ranking"
	outboxusecase "github.com/uchidas-rogue/game-api-sample/internal/usecase/outbox"
	"github.com/uchidas-rogue/game-api-sample/internal/usecase/shared"
)

// AddUserPointsInput はユーザーポイント加算の入力。
type AddUserPointsInput struct {
	UserID int64
	Points int64
	Reason string
}

// GetRankingsInput はランキング取得の入力。
type GetRankingsInput struct {
	Limit  int
	Offset int
}

// RankingsResult はランキング取得の結果。
type RankingsResult struct {
	Rankings   []rankingdomain.RankEntry
	TotalCount int64
}

// Usecase はランキング機能のユースケースインターフェース。
type Usecase interface {
	// GetGuildRankings はギルドランキングを取得する。
	GetGuildRankings(ctx context.Context, input GetRankingsInput) (RankingsResult, error)

	// GetGuildRank は指定ギルドの順位を取得する。
	GetGuildRank(ctx context.Context, guildID int64) (rankingdomain.GuildRankResult, error)

	// AddUserPoints はユーザーのポイントを加算する。
	AddUserPoints(ctx context.Context, input AddUserPointsInput) (rankingdomain.UserPointAddResult, error)

	// GetUserRankings はユーザーランキングを取得する。
	GetUserRankings(ctx context.Context, input GetRankingsInput) (RankingsResult, error)

	// GetUserRank は指定ユーザーの順位を取得する。
	GetUserRank(ctx context.Context, userID int64) (rankingdomain.UserRankResult, error)
}

var _ Usecase = (*usecase)(nil)

type usecase struct {
	tx             shared.Transactor
	repo           Repository
	rankingStore   RankingStore
	outboxRepo     outboxusecase.Repository
	outboxNotifier outboxusecase.Notifier
	logger         *slog.Logger
}

// NewUsecase は Usecase を生成する。
// rankingStore はランキング読み取り（順位・上位 N 件）専用。書き込みは
// AddUserPoints が outboxRepo にイベントを積み、worker が非同期に Redis 反映する。
// outboxNotifier はトランザクションコミット直後に worker へ通知し、ポーリング待ち時間を短縮する。
func NewUsecase(
	tx shared.Transactor,
	repo Repository,
	rankingStore RankingStore,
	outboxRepo outboxusecase.Repository,
	outboxNotifier outboxusecase.Notifier,
	logger *slog.Logger,
) Usecase {
	return &usecase{
		tx:             tx,
		repo:           repo,
		rankingStore:   rankingStore,
		outboxRepo:     outboxRepo,
		outboxNotifier: outboxNotifier,
		logger:         logger,
	}
}

// GetGuildRankings はギルドランキングを取得する。
func (u *usecase) GetGuildRankings(ctx context.Context, input GetRankingsInput) (RankingsResult, error) {
	limit := rankingdomain.NormalizeLimit(input.Limit)
	offset := rankingdomain.NormalizeOffset(input.Offset)

	entries, err := u.rankingStore.GetGuildRankings(ctx, offset, limit)
	if err != nil {
		return RankingsResult{}, fmt.Errorf("get guild rankings: %w", err)
	}

	if len(entries) > 0 {
		guildIDs := make([]int64, len(entries))
		for i, e := range entries {
			guildIDs[i] = e.ID
		}

		guilds, err := u.repo.ListGuildsByIDs(ctx, nil, guildIDs)
		if err != nil {
			return RankingsResult{}, fmt.Errorf("list guilds: %w", err)
		}
		for i := range entries {
			if g, ok := guilds[entries[i].ID]; ok {
				entries[i].Name = g.Name
			}
		}
	}

	totalCount, err := u.rankingStore.GetGuildTotalCount(ctx)
	if err != nil {
		return RankingsResult{}, fmt.Errorf("get total count: %w", err)
	}

	return RankingsResult{
		Rankings:   entries,
		TotalCount: totalCount,
	}, nil
}

// GetGuildRank は指定ギルドの順位を取得する。
func (u *usecase) GetGuildRank(ctx context.Context, guildID int64) (rankingdomain.GuildRankResult, error) {
	guild, err := u.repo.GetGuild(ctx, nil, guildID)
	if err != nil {
		return rankingdomain.GuildRankResult{}, err
	}

	score, exists, err := u.rankingStore.GetGuildScore(ctx, guildID)
	if err != nil {
		return rankingdomain.GuildRankResult{}, fmt.Errorf("get score: %w", err)
	}
	if !exists {
		return rankingdomain.GuildRankResult{}, fmt.Errorf("guild %d: %w", guildID, rankingdomain.ErrScoreNotFound)
	}

	rank, err := u.rankingStore.GetGuildRank(ctx, guildID)
	if err != nil {
		return rankingdomain.GuildRankResult{}, fmt.Errorf("get rank: %w", err)
	}

	totalGuilds, err := u.rankingStore.GetGuildTotalCount(ctx)
	if err != nil {
		return rankingdomain.GuildRankResult{}, fmt.Errorf("get total count: %w", err)
	}

	return rankingdomain.GuildRankResult{
		GuildID:     guild.ID,
		GuildName:   guild.Name,
		Score:       score,
		Rank:        rank,
		TotalGuilds: totalGuilds,
	}, nil
}

// AddUserPoints はユーザーのポイントを加算し、所属ギルドのスコアにも累計加算する。
// MySQL 側の更新（個人ポイント・ギルドスコア・履歴）と Redis 反映用の outbox イベント
// 登録を同一トランザクション内で原子的に実行する。Redis への反映は outbox-worker が
// 非同期にポーリングして行うため、本メソッドは順位（Rank/GuildRank）を返さない。
func (u *usecase) AddUserPoints(ctx context.Context, input AddUserPointsInput) (rankingdomain.UserPointAddResult, error) {
	if !rankingdomain.IsValidScore(input.Points) {
		return rankingdomain.UserPointAddResult{}, fmt.Errorf(
			"%w: %d (allowed: %d-%d)",
			rankingdomain.ErrInvalidPoints, input.Points,
			rankingdomain.MinScore, rankingdomain.MaxScore,
		)
	}

	var result rankingdomain.UserPointAddResult
	err := u.tx.DoInTx(ctx, func(tx shared.Tx) error {
		if _, err := u.repo.GetUser(ctx, tx, input.UserID); err != nil {
			return err
		}

		// 所属ギルドID（未所属は ErrUserNotInGuild）。GvG前提のため必須。
		guildID, err := u.repo.GetUserGuildID(ctx, tx, input.UserID)
		if err != nil {
			return err
		}

		// 個人ポイント現在値（応答の previous_total 用）
		currentPoints, err := u.repo.GetUserPoints(ctx, tx, input.UserID)
		var previousTotal int64
		if err != nil {
			if !errors.Is(err, rankingdomain.ErrPointsNotFound) {
				return err
			}
		} else {
			previousTotal = currentPoints.Points
		}

		// ギルド集計（guild_scores 加算・guild_score_histories 挿入）は outbox-worker へ
		// 非同期化した。ホット行（1ギルド=1行）の排他ロックを同期リクエスト経路から完全に
		// 除去し、同一ギルドへの同時加算がレスポンスを直列化させないようにするため。
		// リクエスト tx では個人系（user_points・user_point_histories）と outbox 記録のみ行う。

		// 個人ポイント履歴（user 行ロックはユーザー毎で分散＝低競合）。
		if err := u.repo.InsertUserPointHistory(ctx, tx, input.UserID, input.Points, input.Reason); err != nil {
			return err
		}

		// 個人ポイント累計加算。
		if err := u.repo.IncrementUserPoints(ctx, tx, input.UserID, input.Points); err != nil {
			return err
		}

		// ギルド集計と Redis 反映用のイベントを outbox に積む。worker が非同期に
		// guild_scores 加算・guild_score_histories 挿入・Redis 反映を行う。
		// payload は差分（Points）と対象（UserID/GuildID）を運ぶ。
		payload := outboxdomain.MarshalRankingScoreAddedPayload(outboxdomain.RankingScoreAddedPayload{
			UserID:  input.UserID,
			GuildID: guildID,
			Points:  input.Points,
		})
		if _, err := u.outboxRepo.InsertEvent(ctx, tx, outboxdomain.EventTypeRankingScoreAdded, payload); err != nil {
			return fmt.Errorf("insert outbox event: %w", err)
		}

		result = rankingdomain.UserPointAddResult{
			UserID:        input.UserID,
			Points:        input.Points,
			PreviousTotal: previousTotal,
			NewTotal:      previousTotal + input.Points,
			GuildID:       guildID,
		}
		return nil
	})
	if err != nil {
		return rankingdomain.UserPointAddResult{}, err
	}

	// トランザクションコミット完了後に worker へ通知する（コミット前だと worker が空振りする）。
	// 通知失敗はリクエストを失敗させない。worker のポーリングがフォールバック。
	if nerr := u.outboxNotifier.Notify(ctx); nerr != nil {
		u.logger.WarnContext(ctx, "outbox notify failed (poll fallback will pick up)",
			slog.Any("error", nerr),
		)
	}

	return result, nil
}

// GetUserRankings はユーザーランキングを取得する。
func (u *usecase) GetUserRankings(ctx context.Context, input GetRankingsInput) (RankingsResult, error) {
	limit := rankingdomain.NormalizeLimit(input.Limit)
	offset := rankingdomain.NormalizeOffset(input.Offset)

	entries, err := u.rankingStore.GetUserRankings(ctx, offset, limit)
	if err != nil {
		return RankingsResult{}, fmt.Errorf("get user rankings: %w", err)
	}

	if len(entries) > 0 {
		userIDs := make([]int64, len(entries))
		for i, e := range entries {
			userIDs[i] = e.ID
		}

		users, err := u.repo.ListUsersByIDs(ctx, nil, userIDs)
		if err != nil {
			return RankingsResult{}, fmt.Errorf("list users: %w", err)
		}
		for i := range entries {
			if name, ok := users[entries[i].ID]; ok {
				entries[i].Name = name
			}
		}
	}

	totalCount, err := u.rankingStore.GetUserTotalCount(ctx)
	if err != nil {
		return RankingsResult{}, fmt.Errorf("get total count: %w", err)
	}

	return RankingsResult{
		Rankings:   entries,
		TotalCount: totalCount,
	}, nil
}

// GetUserRank は指定ユーザーの順位を取得する。
func (u *usecase) GetUserRank(ctx context.Context, userID int64) (rankingdomain.UserRankResult, error) {
	userName, err := u.repo.GetUser(ctx, nil, userID)
	if err != nil {
		return rankingdomain.UserRankResult{}, err
	}

	points, exists, err := u.rankingStore.GetUserPoints(ctx, userID)
	if err != nil {
		return rankingdomain.UserRankResult{}, fmt.Errorf("get points: %w", err)
	}
	if !exists {
		return rankingdomain.UserRankResult{}, fmt.Errorf("user %d: %w", userID, rankingdomain.ErrPointsNotFound)
	}

	rank, err := u.rankingStore.GetUserRank(ctx, userID)
	if err != nil {
		return rankingdomain.UserRankResult{}, fmt.Errorf("get rank: %w", err)
	}

	totalUsers, err := u.rankingStore.GetUserTotalCount(ctx)
	if err != nil {
		return rankingdomain.UserRankResult{}, fmt.Errorf("get total count: %w", err)
	}

	return rankingdomain.UserRankResult{
		UserID:     userID,
		UserName:   userName,
		Points:     points,
		Rank:       rank,
		TotalUsers: totalUsers,
	}, nil
}
