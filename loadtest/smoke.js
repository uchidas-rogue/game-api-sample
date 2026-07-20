// loadtest/smoke.js
// 疎通確認用。低VUで全エンドポイントを一巡し、スクリプトとseedの妥当性を検証する。
// 本格的な負荷をかける前に必ずこれをパスさせること。
//
//   make load/smoke
import http from 'k6/http';
import { sleep } from 'k6';
import {
  BASE_URL,
  jsonParams,
  randUserID,
  randGuildID,
  expectStatus,
} from './lib/common.js';

export const options = {
  vus: Number(__ENV.VUS || 3),
  duration: __ENV.DURATION || '30s',
  thresholds: {
    http_req_failed: ['rate<0.01'],
    http_req_duration: ['p(95)<500'],
  },
};

export default function () {
  const uid = randUserID();
  const gid = randGuildID();

  // health
  expectStatus(http.get(`${BASE_URL}/healthz`), 'health');

  // gacha（10連）
  const gacha = http.post(
    `${BASE_URL}/users/${uid}/gacha/multi`,
    JSON.stringify({ pull_count: 10 }),
    jsonParams,
  );
  expectStatus(gacha, 'gacha');

  // points 加算
  const points = http.post(
    `${BASE_URL}/users/${uid}/points`,
    JSON.stringify({ points: 100, reason: 'smoke' }),
    jsonParams,
  );
  expectStatus(points, 'points');

  // ランキング参照（4系統）
  expectStatus(http.get(`${BASE_URL}/rankings/users?limit=50&offset=0`), 'user_rankings');
  expectStatus(http.get(`${BASE_URL}/rankings/guilds?limit=50&offset=0`), 'guild_rankings');
  expectStatus(http.get(`${BASE_URL}/users/${uid}/ranking`), 'user_rank');
  expectStatus(http.get(`${BASE_URL}/guilds/${gid}/ranking`), 'guild_rank');

  sleep(1);
}
