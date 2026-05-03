package ranking

//go:generate mockgen -source=usecase.go -destination=mock/mock_usecase.go -package=mock_ranking

import (
	"context"
	"fmt"
	"log/slog"

	rankingdomain "github.com/uchidas-rogue/game-api-sample/internal/domain/ranking"
	"github.com/uchidas-rogue/game-api-sample/internal/usecase/shared"
)

// SubmitGuildScoreInput はギルドスコア送信の入力。
type SubmitGuildScoreInput struct {
	GuildID int64
	UserID  int64
	Score   int64
}

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
	// SubmitGuildScore はギルドのスコアを送信する。
	SubmitGuildScore(ctx context.Context, input SubmitGuildScoreInput) (rankingdomain.GuildScoreSubmitResult, error)

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
	tx           shared.Transactor
	repo         Repository
	rankingStore RankingStore
	logger       *slog.Logger
}

// NewUsecase は Usecase を生成する。
func NewUsecase(
	tx shared.Transactor,
	repo Repository,
	rankingStore RankingStore,
	logger *slog.Logger,
) Usecase {
	return &usecase{
		tx:           tx,
		repo:         repo,
		rankingStore: rankingStore,
		logger:       logger,
	}
}

// SubmitGuildScore はギルドのスコアを送信する。
func (u *usecase) SubmitGuildScore(ctx context.Context, input SubmitGuildScoreInput) (rankingdomain.GuildScoreSubmitResult, error) {
	if !rankingdomain.IsValidScore(input.Score) {
		return rankingdomain.GuildScoreSubmitResult{}, fmt.Errorf(
			"%w: %d (allowed: %d-%d)",
			rankingdomain.ErrInvalidScore, input.Score,
			rankingdomain.MinScore, rankingdomain.MaxScore,
		)
	}

	var result rankingdomain.GuildScoreSubmitResult
	err := u.tx.DoInTx(ctx, func(tx shared.Tx) error {
		guild, err := u.repo.GetGuild(ctx, tx, input.GuildID)
		if err != nil {
			return err
		}

		isMember, err := u.repo.IsUserInGuild(ctx, tx, input.UserID, input.GuildID)
		if err != nil {
			return err
		}
		if !isMember {
			return fmt.Errorf("user %d: %w (guild=%d)", input.UserID, rankingdomain.ErrUserNotInGuild, input.GuildID)
		}

		currentScore, err := u.repo.GetGuildScore(ctx, tx, input.GuildID)
		var previousScore int64
		isHighScore := true
		if err != nil {
			if err.Error() != rankingdomain.ErrScoreNotFound.Error() {
				return err
			}
		} else {
			previousScore = currentScore.Score
			isHighScore = input.Score > previousScore
		}

		if err := u.repo.InsertGuildScoreHistory(ctx, tx, input.GuildID, input.UserID, input.Score); err != nil {
			return err
		}

		if isHighScore {
			if err := u.repo.IncrementGuildScore(ctx, tx, input.GuildID, input.Score-previousScore); err != nil {
				return err
			}
			if err := u.rankingStore.IncrementGuildScore(ctx, input.GuildID, input.Score-previousScore); err != nil {
				return err
			}
		}

		rank, err := u.rankingStore.GetGuildRank(ctx, input.GuildID)
		if err != nil {
			return err
		}

		result = rankingdomain.GuildScoreSubmitResult{
			GuildID:       guild.ID,
			Score:         input.Score,
			IsHighScore:   isHighScore,
			PreviousScore: previousScore,
			Rank:          rank,
		}
		return nil
	})
	if err != nil {
		return rankingdomain.GuildScoreSubmitResult{}, err
	}
	return result, nil
}

// GetGuildRankings はギルドランキングを取得する。
func (u *usecase) GetGuildRankings(ctx context.Context, input GetRankingsInput) (RankingsResult, error) {
	limit := rankingdomain.NormalizeLimit(input.Limit)
	offset := input.Offset
	if offset < 0 {
		offset = 0
	}

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

// AddUserPoints はユーザーのポイントを加算する。
func (u *usecase) AddUserPoints(ctx context.Context, input AddUserPointsInput) (rankingdomain.UserPointAddResult, error) {
	if !rankingdomain.IsValidPoints(input.Points) {
		return rankingdomain.UserPointAddResult{}, fmt.Errorf(
			"%w: %d (allowed: %d-%d)",
			rankingdomain.ErrInvalidPoints, input.Points,
			rankingdomain.MinScore, rankingdomain.MaxScore,
		)
	}

	var result rankingdomain.UserPointAddResult
	err := u.tx.DoInTx(ctx, func(tx shared.Tx) error {
		_, err := u.repo.GetUser(ctx, tx, input.UserID)
		if err != nil {
			return err
		}

		currentPoints, err := u.repo.GetUserPoints(ctx, tx, input.UserID)
		var previousTotal int64
		if err != nil {
			if err.Error() != rankingdomain.ErrPointsNotFound.Error() {
				return err
			}
		} else {
			previousTotal = currentPoints.Points
		}

		if err := u.repo.InsertUserPointHistory(ctx, tx, input.UserID, input.Points, input.Reason); err != nil {
			return err
		}

		if err := u.repo.IncrementUserPoints(ctx, tx, input.UserID, input.Points); err != nil {
			return err
		}

		if err := u.rankingStore.IncrementUserPoints(ctx, input.UserID, input.Points); err != nil {
			return err
		}

		rank, err := u.rankingStore.GetUserRank(ctx, input.UserID)
		if err != nil {
			return err
		}

		newTotal := previousTotal + input.Points
		result = rankingdomain.UserPointAddResult{
			UserID:        input.UserID,
			Points:        input.Points,
			PreviousTotal: previousTotal,
			NewTotal:      newTotal,
			Rank:          rank,
		}
		return nil
	})
	if err != nil {
		return rankingdomain.UserPointAddResult{}, err
	}
	return result, nil
}

// GetUserRankings はユーザーランキングを取得する。
func (u *usecase) GetUserRankings(ctx context.Context, input GetRankingsInput) (RankingsResult, error) {
	limit := rankingdomain.NormalizeLimit(input.Limit)
	offset := input.Offset
	if offset < 0 {
		offset = 0
	}

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
