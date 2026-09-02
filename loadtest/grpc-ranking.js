// loadtest/grpc-ranking.js
// ランキング参照（読み取り系）シナリオの gRPC 版。HTTP 版 ranking.js と対になる。
//
// 目的は「同じユースケース・同じ負荷形状で HTTP と gRPC を比較する」こと。
// 呼び出しの内訳と負荷形状（arrivalScenario）を ranking.js と一致させてあるので、
// 両者の p95/p99 と実 RPS を並べればプロトコル差の実測値になる。
// 片方だけ RATE や比率を変えると比較にならないため、変更するときは両方を揃えること。
//
//   make load/grpc
//   RATE=3000 DURATION=3m make load/grpc
//
// 事前に `make load/warm`（DB→Redis 反映）を実行しておくとデータが温まる。
// ランキング未構築のときは HTTP の 503 に相当する codes.Unavailable が返る。
import grpc from 'k6/net/grpc';
import { check } from 'k6';
import { arrivalScenario, defaultGrpcThresholds, grpcCalls, randUserID, randGuildID } from './lib/common.js';

// gRPC サーバのアドレス（make/loadtest.mk の GRPC_ADDR から注入）。
// HTTP 版の BASE_URL と違い scheme を含まない（k6 の gRPC client は host:port を取る）。
const GRPC_ADDR = __ENV.GRPC_ADDR || 'localhost:9090';

// サーバは平文 h2c で listen する（TLS 未対応。clients/unity/README.md 参照）。
const PLAINTEXT = (__ENV.GRPC_PLAINTEXT || 'true') === 'true';

const client = new grpc.Client();

// .proto の読み込みは init コンテキストで 1 回だけ行う（VU ごとの再パースを避ける）。
// import パスはスクリプトからの相対で解決されるため、loadtest/ から見た ../proto を指す。
// proto/ が契約の正本なので、ここに .proto の写しを置かない。
client.load(['../proto'], 'game/ranking/v1/ranking.proto');

const SERVICE = 'game.ranking.v1.RankingService';

export const options = {
  scenarios: arrivalScenario({ rate: 1000 }),
  thresholds: defaultGrpcThresholds,
};

// gRPC は HTTP/1.1 と違い 1 本の接続を多重化して使う。毎イテレーションで connect すると
// 接続確立（TCP + HTTP/2 ハンドシェイク）のコストを測ってしまい、HTTP 版との比較が壊れる。
// k6 の作法どおり VU ごとに 1 回だけ張り、以降は張りっぱなしで使い回す。
// モジュールスコープの変数は VU ごとに独立した JS ランタイムに属するため、VU 間で共有されない。
// 明示的な close は置かない。teardown() は専用の VU で走り、そこの client は connect していないため
// close の呼び先が無い。VU の接続はテスト終了時に k6 が破棄する。
let connected = false;

function ensureConnected() {
  if (connected) {
    return;
  }
  client.connect(GRPC_ADDR, { plaintext: PLAINTEXT });
  connected = true;
}

// 応答の gRPC ステータスを検証する。name はチェック名兼メトリクスタグ。
// HTTP 版の expectStatus と役割は同じだが、見るのは http status ではなく gRPC status code。
function expectOK(res, name) {
  // status の成否に関わらず「サーバへ到達して応答が返った」ことを数える。
  // connect に失敗するとここへ来ないので、これが0件なら全滅と判定できる
  // （defaultGrpcThresholds の grpc_calls: count>0 が拾う）。
  grpcCalls.add(1);

  return check(res, {
    [`${name}: status OK`]: (r) => r && r.status === grpc.StatusOK,
  });
}

// 参照系はページング一覧と個別順位を混在させ、実利用に近い読み取りパターンにする。
// 比率は ranking.js と同一（一覧 35% + 35%、個別 15% + 15%）。
//
// streaming（WatchUserRankings）はこのシナリオに含めない。理由は loadtest/README.md を参照。
export default function () {
  ensureConnected();

  const r = Math.random();
  if (r < 0.35) {
    const offset = Math.floor(Math.random() * 10) * 50;
    const res = client.invoke(`${SERVICE}/GetUserRankings`, { limit: 50, offset: offset }, { tags: { name: 'user_rankings' } });
    expectOK(res, 'user_rankings');
  } else if (r < 0.7) {
    const offset = Math.floor(Math.random() * 10) * 50;
    const res = client.invoke(`${SERVICE}/GetGuildRankings`, { limit: 50, offset: offset }, { tags: { name: 'guild_rankings' } });
    expectOK(res, 'guild_rankings');
  } else if (r < 0.85) {
    const res = client.invoke(`${SERVICE}/GetUserRank`, { user_id: randUserID() }, { tags: { name: 'user_rank' } });
    expectOK(res, 'user_rank');
  } else {
    const res = client.invoke(`${SERVICE}/GetGuildRank`, { guild_id: randGuildID() }, { tags: { name: 'guild_rank' } });
    expectOK(res, 'guild_rank');
  }
}
