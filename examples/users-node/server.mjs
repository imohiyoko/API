// Go を使わない本体の例（roadmap.md M3）。examples/users と同じ操作を Node.js で実装し、
// examples/users/api/manifest.yaml とサイドカー（api sidecar）で同じ API を出す。
//
//   PORT=3000 API_SIDECAR_UPSTREAM_TOKEN=secret node examples/users-node/server.mjs
//   api -manifest examples/users/api/manifest.yaml sidecar -dev -upstream http://127.0.0.1:3000 -listen internal=127.0.0.1:8190
//
// サイドカーが認証・認可・入力の検証・フィールドの絞り込みをするので、本体は検証済みの入力を受け、
// 全部のフィールドを返せばよい（公開範囲に見えないものはサイドカーが捨てる）。依存のパッケージは無い。
import { createServer } from "node:http";

const token = process.env.API_SIDECAR_UPSTREAM_TOKEN ?? "";
const users = [];
let next = 0; // 採番。削除しても戻さない。

class APIError extends Error {
  constructor(code, message, status) {
    super(message);
    this.code = code;
    this.status = status;
  }
}
const notFound = (id) => new APIError("not_found", `user ${id}`, 404);

// 操作ごとの処理。input はサイドカーが確かめた後のもの。principal は認証済みの主体（匿名なら null）。
const operations = {
  "users.get": ({ input }) => {
    const u = users.find((u) => u.id === input.id);
    if (!u) throw notFound(input.id);
    return u;
  },
  "users.list": ({ input }) => {
    let n = users.length;
    if (input.limit !== undefined && input.limit !== null && input.limit < n) n = Math.max(input.limit, 0);
    return users.slice(0, n);
  },
  "users.create": ({ input }) => {
    next++;
    const u = { id: `usr_${next}`, name: input.name, email: input.email, note: input.note ?? "" };
    users.push(u);
    return u;
  },
  "users.delete": ({ input }) => {
    const i = users.findIndex((u) => u.id === input.id);
    if (i < 0) throw notFound(input.id);
    users.splice(i, 1);
    return {};
  },
};

function reply(res, status, body) {
  res.writeHead(status, { "Content-Type": "application/json" });
  res.end(JSON.stringify(body));
}

createServer((req, res) => {
  // サイドカーからの要求だけを受ける。
  if (token && req.headers.authorization !== `Bearer ${token}`) {
    return reply(res, 401, { error: { code: "unauthenticated", message: "not from the sidecar" } });
  }
  const op = operations[req.url.slice(1)];
  if (req.method !== "POST" || !op) {
    return reply(res, 404, { error: { code: "not_found", message: "no such operation" } });
  }
  let raw = "";
  req.setEncoding("utf8");
  req.on("data", (chunk) => (raw += chunk));
  req.on("end", () => {
    try {
      const output = op(JSON.parse(raw));
      reply(res, 200, { output });
    } catch (e) {
      if (e instanceof APIError) return reply(res, e.status, { error: { code: e.code, message: e.message } });
      console.error(e);
      reply(res, 500, { error: { code: "internal", message: "internal error" } });
    }
  });
}).listen(Number(process.env.PORT ?? 3000), "127.0.0.1", () => console.log("users-node ready"));
