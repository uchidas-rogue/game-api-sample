// loadtest/lib/common.js
// 各シナリオで共有する設定・ヘルパ。
//
// 環境変数（make/loadtest.mk から注入）:
//   BASE_URL  対象APIのベースURL（既定 http://localhost:8080）
//   USERS     seed で投入したユーザー数（ID空間 1..USERS）
//   GUILDS    seed で投入したギルド数（ID空間 1..GUILDS）
// 負荷形状の上書き:
//   RATE       維持する目標RPS
//   START_RATE ramp 開始RPS
//   RAMP       ramp-up 時間（例 30s）
//   DURATION   維持時間（例 1m）
//   PRE_VUS    事前確保VU数
//   MAX_VUS    最大VU数
import { check } from 'k6';
import { Counter } from 'k6/metrics';

export const BASE_URL = __ENV.BASE_URL || 'http://localhost:8080';
export const USERS = parseInt(__ENV.USERS || '10000', 10);
export const GUILDS = parseInt(__ENV.GUILDS || '100', 10);

export const jsonParams = { headers: { 'Content-Type': 'application/json' } };

// seed 済みID空間からランダムに1件選ぶ（1-origin）。
export function randUserID() {
  return Math.floor(Math.random() * USERS) + 1;
}
export function randGuildID() {
  return Math.floor(Math.random() * GUILDS) + 1;
}

// 共通の合否基準。参照系/更新系で使い回す。
export const defaultThresholds = {
  http_req_failed: ['rate<0.01'], // エラー率 1% 未満
  http_req_duration: ['p(95)<200', 'p(99)<500'], // p95<200ms, p99<500ms
};

// gRPC 用の合否基準。HTTP 版と同じ水準（p95<200ms / p99<500ms、エラー率1%未満）を
// gRPC のメトリクス名に読み替えたもの。
//
// defaultThresholds を gRPC シナリオに流用してはいけない。k6 の gRPC 呼び出しは
// http_req_* を一切出さないため、閾値が「対象0件」で常に無条件パスし、
// 遅延もエラーも検出しないまま緑になる。
//
// エラー率は grpc_req_failed に相当するメトリクスが無いので checks で見る
// （各シナリオ側で invoke の status を check し、その成功率を判定する）。
//
// grpcCalls は「1件も測れていないのに緑になる」のを防ぐ番人。gRPC シナリオは invoke の結果を
// 受け取るたびにこれを add する（status の成否は問わない。サーバへ到達できたかどうかを数える）。
//
// なぜ要るか（実測で再現した挙動）:
//   サーバが落ちていると client.connect() が毎イテレーション例外を投げ、invoke まで到達しない。
//   すると checks も grpc_req_duration も**サンプル0件**になる。k6 はサンプル0件の
//   rate / p(95) 閾値を無条件パスさせるため、**全イテレーション失敗なのに exit code 0** になる。
//   Counter の count>0 だけはサンプル0件で正しく落ちるので、これを最後の番人に使う。
//   （grpc_req_duration に count>0 は付けられない。k6 の Trend が対応する集計は
//     avg / min / max / med / p のみで、count を指定すると閾値の設定エラーになる）
//
// common.js を import した時点でメトリクスは登録されるが、HTTP シナリオの summary には出ない
// （k6 がサンプル0件の行を出すのは閾値が付いているときだけ。実測で確認済み）。HTTP 側への影響は無い。
export const grpcCalls = new Counter('grpc_calls');

export const defaultGrpcThresholds = {
  grpc_calls: ['count>0'], // 1件も到達していなければ失敗（サンプル0件での空パス防止）
  checks: ['rate>0.99'], // check 成功率 99% 超 = エラー率 1% 未満
  grpc_req_duration: ['p(95)<200', 'p(99)<500'], // p95<200ms, p99<500ms
};

// RPS駆動（ramping-arrival-rate）シナリオを環境変数で組み立てる。
// VU駆動と違い、サーバが遅くなっても目標RPSを維持しようとするため実負荷計測に向く。
export function arrivalScenario(defaults = {}) {
  const rate = Number(__ENV.RATE || defaults.rate || 500);
  const startRate = Number(__ENV.START_RATE || defaults.startRate || 50);
  const ramp = __ENV.RAMP || defaults.ramp || '30s';
  const duration = __ENV.DURATION || defaults.duration || '1m';
  const preVUs = Number(__ENV.PRE_VUS || defaults.preVUs || 200);
  const maxVUs = Number(__ENV.MAX_VUS || defaults.maxVUs || 1000);

  return {
    main: {
      executor: 'ramping-arrival-rate',
      startRate,
      timeUnit: '1s',
      preAllocatedVUs: preVUs,
      maxVUs,
      stages: [
        { target: rate, duration: ramp },
        { target: rate, duration: duration },
        { target: 0, duration: '10s' },
      ],
    },
  };
}

// レスポンスのステータス検証。name はメトリクスタグ兼チェック名。
export function expectStatus(res, name, status = 200) {
  return check(res, {
    [`${name}: status ${status}`]: (r) => r.status === status,
  });
}
