package ranking_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	rankingdomain "github.com/uchidas-rogue/game-api-sample/internal/domain/ranking"
	rankinghandler "github.com/uchidas-rogue/game-api-sample/internal/driver/http/ranking"
	"github.com/uchidas-rogue/game-api-sample/internal/testutil/apicontract"
	"github.com/uchidas-rogue/game-api-sample/internal/testutil/slogtest"
	rankingusecase "github.com/uchidas-rogue/game-api-sample/internal/usecase/ranking"
	mockranking "github.com/uchidas-rogue/game-api-sample/internal/usecase/ranking/mock"
)

// 契約ファイル（レスポンス構造の正本）へのパス。
// go test の作業ディレクトリはパッケージディレクトリなので相対パスで届く。
const (
	contractRankings      = "../testdata/contracts/ranking_rankings.json"
	contractGuildRank     = "../testdata/contracts/ranking_guild_rank.json"
	contractUserRank      = "../testdata/contracts/ranking_user_rank.json"
	contractAddUserPoints = "../testdata/contracts/ranking_add_user_points.json"
	contractError         = "../testdata/contracts/error.json"
)

// endpoint は契約検証の対象エンドポイント。
type endpoint int

const (
	epGuildRankings endpoint = iota
	epUserRankings
	epGuildRank
	epUserRank
	epAddUserPoints
	epError
)

// TestResponseContract は各エンドポイントのレスポンスの **構造** が契約ファイルと一致することを検証する。
//
// 値の妥当性はハンドラのテストの責務なので、ここでは見ない。
// 見るのは「キー名の変更・追加・削除が起きていないこと」だけ。
// 契約が複数のバリエーションを持つ場合でも、構造が同一なら代表1パターンでよい。
//
// docs/testing/http-ranking.md「5. レスポンス契約」の表と 1 対 1 で対応する。
func TestResponseContract(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		contractPath string
		ep           endpoint
		wantStatus   int
	}{
		{name: "ギルドランキング一覧", contractPath: contractRankings, ep: epGuildRankings, wantStatus: http.StatusOK},
		{name: "ユーザーランキング一覧", contractPath: contractRankings, ep: epUserRankings, wantStatus: http.StatusOK},
		{name: "ギルド順位", contractPath: contractGuildRank, ep: epGuildRank, wantStatus: http.StatusOK},
		{name: "ユーザー順位", contractPath: contractUserRank, ep: epUserRank, wantStatus: http.StatusOK},
		{name: "ポイント加算", contractPath: contractAddUserPoints, ep: epAddUserPoints, wantStatus: http.StatusOK},
		{name: "エラーレスポンス", contractPath: contractError, ep: epError, wantStatus: http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := execContractRequest(t, tt.ep)
			require.Equal(t, tt.wantStatus, rec.Code, "契約検証の前提: 想定したステータスであること body=%s", rec.Body.String())
			apicontract.AssertStructure(t, tt.contractPath, rec.Body.Bytes())
		})
	}
}

func execContractRequest(t *testing.T, ep endpoint) *httptest.ResponseRecorder {
	t.Helper()

	ctrl := gomock.NewController(t)
	uc := mockranking.NewMockUsecase(ctrl)
	h := rankinghandler.NewHandler(uc, slogtest.NewLogger(t, nil))
	e := echo.New()

	switch ep {
	case epGuildRankings:
		uc.EXPECT().GetGuildRankings(gomock.Any(), gomock.Any()).Return(rankingusecase.RankingsResult{
			Rankings:   []rankingdomain.RankEntry{{Rank: 1, ID: 1, Name: "ギルドA", Score: 9000}},
			TotalCount: 1,
		}, nil)
		c, rec := newEchoContext(e, http.MethodGet, "/rankings/guilds?limit=10&offset=0", "")
		require.NoError(t, h.GetGuildRankings(c))
		return rec

	case epUserRankings:
		uc.EXPECT().GetUserRankings(gomock.Any(), gomock.Any()).Return(rankingusecase.RankingsResult{
			Rankings:   []rankingdomain.RankEntry{{Rank: 1, ID: 1, Name: "ユーザーA", Score: 9000}},
			TotalCount: 1,
		}, nil)
		c, rec := newEchoContext(e, http.MethodGet, "/rankings/users?limit=10&offset=0", "")
		require.NoError(t, h.GetUserRankings(c))
		return rec

	case epGuildRank:
		uc.EXPECT().GetGuildRank(gomock.Any(), int64(1)).Return(rankingdomain.GuildRankResult{
			GuildID: 1, GuildName: "ギルドA", Score: 9000, Rank: 1, TotalGuilds: 10,
		}, nil)
		c, rec := newEchoContext(e, http.MethodGet, "/guilds/1/ranking", "")
		c.SetParamNames("guildID")
		c.SetParamValues("1")
		require.NoError(t, h.GetGuildRank(c))
		return rec

	case epUserRank:
		uc.EXPECT().GetUserRank(gomock.Any(), int64(1)).Return(rankingdomain.UserRankResult{
			UserID: 1, UserName: "ユーザーA", Points: 9000, Rank: 1, TotalUsers: 10,
		}, nil)
		c, rec := newEchoContext(e, http.MethodGet, "/users/1/ranking", "")
		c.SetParamNames("userID")
		c.SetParamValues("1")
		require.NoError(t, h.GetUserRank(c))
		return rec

	case epAddUserPoints:
		uc.EXPECT().AddUserPoints(gomock.Any(), gomock.Any()).Return(rankingdomain.UserPointAddResult{
			UserID: 1, Points: 100, PreviousTotal: 0, NewTotal: 100,
			GuildID: 2, GuildPreviousTotal: 0, GuildNewTotal: 100,
		}, nil)
		c, rec := newEchoContext(e, http.MethodPost, "/users/1/points", `{"points":100,"reason":"test"}`)
		c.SetParamNames("userID")
		c.SetParamValues("1")
		require.NoError(t, h.AddUserPoints(c))
		return rec

	case epError:
		// 入力バリデーションで弾かれる経路。usecase には到達しない。
		c, rec := newEchoContext(e, http.MethodGet, "/rankings/guilds?limit=-1", "")
		require.NoError(t, h.GetGuildRankings(c))
		return rec
	}

	t.Fatalf("未知の endpoint: %d", ep)
	return nil
}
