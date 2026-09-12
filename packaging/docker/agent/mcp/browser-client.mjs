import http from "node:http";
import { createHash } from "node:crypto";
import { spawn } from "node:child_process";
import { fileURLToPath } from "node:url";

export function socketPath(sessionID) {
  if (!sessionID) throw new Error("BRIGADE_SESSION_ID is required");
  return `/tmp/brigade-browser-${process.getuid()}-${createHash("sha256").update(sessionID).digest("hex").slice(0, 24)}.sock`;
}

export function requestBrowser(socket, role, body) {
  return new Promise((resolve, reject) => {
    const req = http.request({ socketPath: socket, path: `/${role}`, method: "POST", agent: false, headers: { "Content-Type": "application/json" } }, res => {
      let data = "";
      res.setEncoding("utf8");
      res.on("data", chunk => { data += chunk; });
      res.on("end", () => {
        try {
          const value = JSON.parse(data);
          if (res.statusCode !== 200) reject(new Error(value.error ?? "Browser request failed"));
          else resolve(value);
        } catch (error) { reject(error); }
      });
      res.on("error", reject);
    });
    req.setTimeout(45000, () => req.destroy(new Error("Browser request timed out")));
    req.on("error", reject);
    req.end(JSON.stringify(body));
  });
}

export async function browserTool(args, handoff = false) {
  const sessionID = process.env.BRIGADE_SESSION_ID;
  const socket = socketPath(sessionID);
  try {
    await requestBrowser(socket, "agent", { action: "ping" });
  } catch (error) {
    if (!["ENOENT", "ECONNREFUSED"].includes(error.code)) throw error;
    const child = spawn(process.execPath, [fileURLToPath(new URL("./browser-server.mjs", import.meta.url)), sessionID], {
      detached: true, stdio: "ignore", env: process.env,
    });
    let spawnError;
    child.once("error", error => { spawnError = error; });
    child.unref();
    let started = false;
    for (let i = 0; i < 50; i++) {
      await new Promise(resolve => setTimeout(resolve, 100));
      if (spawnError) throw spawnError;
      try { await requestBrowser(socket, "agent", { action: "ping" }); started = true; break; }
      catch (err) { if (!["ENOENT", "ECONNREFUSED"].includes(err.code)) throw err; }
    }
    if (!started) throw new Error("Не удалось запустить браузерный сервис. Обновите runtime сессии.");
  }
  return requestBrowser(socket, "agent", { ...args, action: handoff ? "handoff" : args.action });
}
