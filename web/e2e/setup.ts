// 启动一个只供浏览器测试使用的后台:临时数据目录、随机端口。
// 地址和令牌通过环境变量交给测试,测试结束后停掉后台并删除数据目录。
import { spawn } from "node:child_process";
import { existsSync, mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { setTimeout as sleep } from "node:timers/promises";

export default async function setup() {
  const bin = resolve(process.env.BUGU_BIN ?? "../bin/bugu");
  if (!existsSync(bin)) throw new Error(`找不到 ${bin},先运行 make build,或用 BUGU_BIN 指定程序`);

  const home = mkdtempSync(join(tmpdir(), "bugu-e2e-"));
  const server = spawn(bin, ["serve", "--listen", "127.0.0.1:0"], {
    env: { ...process.env, BUGU_HOME: home },
    stdio: ["ignore", "ignore", "pipe"],
  });
  let log = "";
  server.stderr.on("data", (chunk) => (log += chunk));

  const endpoint = join(home, "endpoint.json");
  for (let i = 0; !existsSync(endpoint); i++) {
    if (server.exitCode !== null || i > 100) {
      server.kill();
      throw new Error("后台没有启动:\n" + log);
    }
    await sleep(100);
  }
  const { addr } = JSON.parse(readFileSync(endpoint, "utf8")) as { addr: string };
  process.env.BUGU_URL = `http://${addr}`;
  process.env.BUGU_TOKEN = readFileSync(join(home, "token"), "utf8").trim();

  return async () => {
    server.kill("SIGTERM");
    await new Promise((done) => (server.exitCode !== null ? done(null) : server.once("exit", done)));
    rmSync(home, { recursive: true, force: true });
  };
}
