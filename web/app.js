// このリポジトリについて答えるチャットのクライアント。
//
// 【流れ】
//   1. web/data/index.json（文書から生成した索引）をブラウザで読む
//   2. 質問を BM25 で検索し、関連する上位チャンクを選ぶ
//   3. **チャンク ID だけ** をプロキシへ送る（本文は送らない）
//   4. プロキシが同じ索引から本文を解決してモデルへ渡し、回答をストリームで返す
//
// 本文をクライアントから送らないのは、任意テキストを流し込んで無料の LLM として使われるのを防ぐため。
// プロキシが未設定・到達不能なときは、検索でヒットした文書の抜粋をそのまま表示する
// （チャットが落ちてもページが壊れて見えないように）。

// モデルへ渡すチャンク数と質問の最大文字数は索引（data/index.json の limits）から読む。
// 同じ索引をプロキシもバンドルするので、両者の上限が食い違うことが原理的に起きない。
// 正本は scripts/sitegen の const で、ここに既定値を置かない（置くと写しが復活する）。
const MAX_SOURCES = 4;    // 画面に出す出典の数（画面だけの都合なので索引には載せない）

// MIN_SCORE と RELATIVE_CUTOFF は「無関係な質問」を検索の時点で落とすための閾値。
// bigram 検索は助詞（「の」「は」）だけでも 5 前後のスコアが付くため、それを超えたものだけを
// 当たりとみなす。閾値に達しなければモデルを呼ばずに終わる（無駄なコストを掛けない）。
// 現在の索引に対して当てた経験値なので、索引が大きく変わったら見直す。
const MIN_SCORE = 6;
const RELATIVE_CUTOFF = 0.3;

const state = {
  chunks: [],
  repo: "",        // 索引から入れる（scripts/sitegen が正本。既定値を置くと写しになる）
  branch: "",
  limits: null,    // { topK, maxQuestionChars }。索引を読むまで質問を受け付けない
  df: new Map(),   // トークン -> 出現チャンク数
  avgdl: 0,
  busy: false,
};

const el = {
  log: document.getElementById("log"),
  form: document.getElementById("composer"),
  input: document.getElementById("question"),
  send: document.getElementById("send"),
  status: document.getElementById("status"),
  chips: document.getElementById("chips"),
};

const endpoint = document.querySelector('meta[name="chat-endpoint"]')?.content?.trim() ?? "";

// ---- 索引の読み込みと検索 -------------------------------------------------

// tokenize は日本語を bigram、英数字を語単位に分割する。
// 形態素解析を入れないのは、辞書を持つと索引の何倍もの重さになり、静的サイトの利点が消えるため。
function tokenize(text) {
  const tokens = [];
  const normalized = text.toLowerCase();

  for (const word of normalized.match(/[a-z0-9_.\-/]+/g) ?? []) {
    if (word.length > 1) tokens.push(word);
  }

  const cjk = normalized.replace(/[^぀-ヿ㐀-鿿]+/g, " ");
  for (const run of cjk.split(" ")) {
    if (run.length === 1) tokens.push(run);
    for (let i = 0; i + 1 < run.length; i++) tokens.push(run.slice(i, i + 2));
  }
  return tokens;
}

function buildIndex(chunks) {
  let total = 0;
  for (const chunk of chunks) {
    chunk.tokens = tokenize(`${chunk.trail}\n${chunk.text}`);
    chunk.length = chunk.tokens.length;
    chunk.tf = new Map();
    for (const token of chunk.tokens) {
      chunk.tf.set(token, (chunk.tf.get(token) ?? 0) + 1);
    }
    for (const token of new Set(chunk.tokens)) {
      state.df.set(token, (state.df.get(token) ?? 0) + 1);
    }
    total += chunk.length;
  }
  state.avgdl = total / Math.max(chunks.length, 1);
}

// search は BM25 で上位 k チャンクを返す。
function search(query, k) {
  const K1 = 1.5;
  const B = 0.75;
  const N = state.chunks.length;
  const queryTokens = new Set(tokenize(query));

  const scored = [];
  for (const chunk of state.chunks) {
    let score = 0;
    for (const token of queryTokens) {
      const tf = chunk.tf.get(token);
      if (!tf) continue;
      const df = state.df.get(token) ?? 0;
      const idf = Math.log(1 + (N - df + 0.5) / (df + 0.5));
      score += idf * ((tf * (K1 + 1)) / (tf + K1 * (1 - B + B * (chunk.length / state.avgdl))));
    }
    if (score === 0) continue;

    // 見出しに直接当たった節を底上げする（節タイトルは本文より情報量が濃い）。
    if (tokenize(chunk.title).some((token) => queryTokens.has(token))) {
      score *= 1.15;
    }
    scored.push({ chunk, score });
  }

  scored.sort((a, b) => b.score - a.score);
  if (scored.length === 0 || scored[0].score < MIN_SCORE) return [];

  const floor = scored[0].score * RELATIVE_CUTOFF;
  return scored
    .filter((entry) => entry.score >= floor)
    .slice(0, k)
    .map((entry) => entry.chunk);
}

function sourceURL(chunk) {
  return `https://github.com/${state.repo}/blob/${state.branch}/${chunk.file}#${encodeURIComponent(chunk.anchor)}`;
}

// ---- 表示 ------------------------------------------------------------------

function addMessage(kind, text) {
  const node = document.createElement("div");
  node.className = `msg ${kind}`;
  node.textContent = text;
  el.log.appendChild(node);
  node.scrollIntoView({ behavior: "smooth", block: "nearest" });
  return node;
}

function addSources(node, chunks) {
  if (chunks.length === 0) return;

  const box = document.createElement("div");
  box.className = "sources";
  box.innerHTML = "<p>出典</p>";

  const list = document.createElement("ol");
  const seen = new Set();
  for (const chunk of chunks) {
    const key = `${chunk.file}#${chunk.anchor}`;
    if (seen.has(key)) continue;
    seen.add(key);
    if (seen.size > MAX_SOURCES) break;

    const item = document.createElement("li");
    const link = document.createElement("a");
    link.href = sourceURL(chunk);
    link.textContent = chunk.trail;
    link.rel = "noopener";
    item.appendChild(link);
    list.appendChild(item);
  }
  box.appendChild(list);
  node.appendChild(box);
}

// showExcerpts はプロキシを使わずに、検索でヒットした本文をそのまま見せる。
function showExcerpts(node, chunks, reason) {
  node.textContent = reason;
  for (const chunk of chunks.slice(0, 2)) {
    const excerpt = document.createElement("div");
    excerpt.className = "excerpt";
    excerpt.textContent = chunk.text.slice(0, 400);
    node.appendChild(excerpt);
  }
  addSources(node, chunks);
}

// ---- チャット --------------------------------------------------------------

async function ask(question) {
  // limits が無い = 索引をまだ読めていない。送信ボタンは無効にしてあるが、
  // 読み込み中に Enter で送られる経路が残るのでここでも止める。
  if (state.busy || state.limits === null || question.length === 0) return;
  if (question.length > state.limits.maxQuestionChars) {
    addMessage("error", `質問は ${state.limits.maxQuestionChars} 文字までにしてください。`);
    return;
  }

  state.busy = true;
  el.send.disabled = true;
  addMessage("user", question);

  const hits = search(question, state.limits.topK);
  const answer = addMessage("bot", "");

  if (hits.length === 0) {
    answer.textContent = "リポジトリの文書に該当箇所が見つかりませんでした。別の言い方で試してみてください。";
    finish();
    return;
  }

  if (endpoint === "") {
    showExcerpts(answer, hits, "チャットの中継先が未設定のため、関連する文書の抜粋を表示します。");
    finish();
    return;
  }

  answer.textContent = "…";
  try {
    const response = await fetch(endpoint, {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ question, chunkIds: hits.map((chunk) => chunk.id) }),
    });

    if (!response.ok) {
      // 回答が得られなくても、検索でヒットした本文は手元にある。エラーだけ出して終わるより
      // 抜粋を見せたほうが読み手の役に立つので、ネットワーク断のとき（下の catch）と同じ扱いにする。
      //
      // 理由は必ず本文に残す。とくに「参照する文書を特定できませんでした」は、サイトの索引を
      // 更新したあと Worker の再デプロイを忘れた状態で出る。デプロイが手動である以上、
      // この表示が運用側の唯一の気づく手がかりになるので、黙って抜粋へ落とさない。
      const detail = await response.json().catch(() => ({}));
      const reason = detail.message ?? `チャットが応答しませんでした（${response.status}）。`;
      showExcerpts(answer, hits, `${reason} 関連する文書の抜粋を表示します。`);
      finish();
      return;
    }

    await streamInto(answer, response);
    addSources(answer, hits);
  } catch {
    showExcerpts(answer, hits, "チャットに接続できませんでした。関連する文書の抜粋を表示します。");
  }
  finish();
}

// streamInto は SSE（data: {json} の連なり）を読み進めて本文へ追記する。
async function streamInto(node, response) {
  const reader = response.body.getReader();
  const decoder = new TextDecoder();
  let buffer = "";
  let text = "";

  for (;;) {
    const { value, done } = await reader.read();
    if (done) break;
    buffer += decoder.decode(value, { stream: true });

    const events = buffer.split("\n\n");
    buffer = events.pop() ?? "";
    for (const event of events) {
      for (const line of event.split("\n")) {
        if (!line.startsWith("data:")) continue;
        const payload = JSON.parse(line.slice(5).trim());
        if (payload.type === "delta") {
          text += payload.text;
          node.textContent = text;
        } else if (payload.type === "error") {
          node.className = "msg error";
          node.textContent = payload.message;
        }
      }
    }
  }
  if (text === "" && node.textContent === "…") {
    node.textContent = "回答が空でした。もう一度試してください。";
  }
}

function finish() {
  state.busy = false;
  el.send.disabled = false;
  el.input.value = "";
  el.input.focus();
}

// ---- 起動 ------------------------------------------------------------------

el.form.addEventListener("submit", (event) => {
  event.preventDefault();
  ask(el.input.value.trim());
});

el.chips.addEventListener("click", (event) => {
  if (event.target.classList.contains("chip")) ask(event.target.textContent.trim());
});

fetch("data/index.json")
  .then((response) => response.json())
  .then((data) => {
    state.chunks = data.chunks;
    state.repo = data.repo;
    state.branch = data.branch;
    state.limits = data.limits;
    buildIndex(state.chunks);
    el.input.maxLength = state.limits.maxQuestionChars;
    el.send.disabled = false;
    el.status.textContent = `${state.chunks.length} 個の節を検索できます。回答は文書の該当箇所のみを根拠にしています。`;
  })
  .catch(() => {
    el.status.textContent = "索引の読み込みに失敗しました。GitHub のリポジトリを直接参照してください。";
    el.send.disabled = true;
  });
