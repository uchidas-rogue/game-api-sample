// loadtest/ranking.js
// ランキング参照（読み取り系）シナリオ。Redis ZSet 参照が主。
// 参照負荷が書き込みDB(Aurora)を圧迫しないCQRS設計の検証対象。
// 事前に `make load/warm`（DB→Redis反映）を実行しておくとデータが温まる。
//
//   make load/ranking
//   RATE=3000 DURATION=3m make load/ranking
import http from 'k6/http';
import { arrivalScenario, defaultThresholds, BASE_URL, randUserID, randGuildID } from './lib/common.js';

export const options = {
  scenarios: arrivalScenario({ rate: 1000 }),
  thresholds: defaultThresholds,
};

// 参照系はページング一覧と個別順位を混在させ、実利用に近い読み取りパターンにする。
export default function () {
  const r = Math.random();
  if (r < 0.35) {
    const offset = Math.floor(Math.random() * 10) * 50;
    http.get(`${BASE_URL}/rankings/users?limit=50&offset=${offset}`, { tags: { name: 'user_rankings' } });
  } else if (r < 0.7) {
    const offset = Math.floor(Math.random() * 10) * 50;
    http.get(`${BASE_URL}/rankings/guilds?limit=50&offset=${offset}`, { tags: { name: 'guild_rankings' } });
  } else if (r < 0.85) {
    http.get(`${BASE_URL}/users/${randUserID()}/ranking`, { tags: { name: 'user_rank' } });
  } else {
    http.get(`${BASE_URL}/guilds/${randGuildID()}/ranking`, { tags: { name: 'guild_rank' } });
  }
}
