#!/usr/bin/env node
'use strict';
/*
 * Qoder CN 登录控制进程（本仓自建，非 qoder-proxy 上游组件）。
 *
 * 只监听 127.0.0.1，与 qoder-proxy 共享 console 的网络命名空间，
 * 仅供 console 服务端调用，封装 qoderclicn 的设备码 OAuth：
 *
 *   GET  /healthz            存活探测（无需鉴权）
 *   GET  /status             qoderclicn status -o json（{logged_in,...}，短缓存）
 *   POST /login/start        发起设备码登录，返回 {state, auth_url}
 *   GET  /login              轮询登录进程 {state: idle|waiting|success|error, ...}
 *   POST /logout             删除本地凭据
 *
 * 环路防护（与 qoder-proxy/auth.js 同一思路）：
 *   - 浏览器跨域请求必然带 Origin / 无法带自定义头，因此拒绝任何带 Origin
 *     的请求，并要求 X-Qoder-Ctl: 1（自定义头触发 CORS 预检，本服务不处理预检）；
 *   - 配置了 CONTROL_API_KEY 时额外要求 Bearer 密钥（console 侧 WB2A_QODER_KEY）。
 */
const http = require('http');
const { spawn } = require('child_process');
const fs = require('fs');
const path = require('path');
const crypto = require('crypto');

const HOST = '127.0.0.1';
const PORT = Number(process.env.CONTROL_PORT || 3001);
const API_KEY = process.env.CONTROL_API_KEY || '';
const CLI = process.env.QODERCN_CLI_PATH || process.env.CLI_COMMAND || 'qoderclicn';
const CONFIG_DIR = process.env.QODER_CONFIG_DIR ||
  path.join(process.env.USERPROFILE || process.env.HOME || '/root', '.qoderworkcn');
const STATUS_TTL_MS = 3000;
const LOG_TAIL = 4000;

let loginChild = null;
let loginState = 'idle'; // idle | waiting | success | error
let authUrl = '';
let detail = '';
let finishedAt = 0;
let logTail = '';

let statusCache = null; // {at, promise}

function appendLog(chunk) {
  logTail = (logTail + chunk).slice(-LOG_TAIL);
}

function setLoginState(state, extra = {}) {
  loginState = state;
  if (extra.authUrl !== undefined) authUrl = extra.authUrl;
  if (extra.detail !== undefined) detail = extra.detail;
  if (state === 'success' || state === 'error') finishedAt = Date.now();
}

function extractUrl(text) {
  const m = text.match(/https:\/\/[^\s]+\/device\/selectAccounts\S*/);
  return m ? m[0] : '';
}

function startLogin() {
  // 等待中的进程单飞复用；已结束（idle/success/error）则重新发起。
  if (loginState === 'waiting' && loginChild) return { reused: true };
  if (loginChild) {
    try { loginChild.kill('SIGTERM'); } catch { /* ignore */ }
    loginChild = null;
  }
  logTail = '';
  setLoginState('waiting', { authUrl: '', detail: '' });
  const child = spawn(CLI, ['login'], {
    stdio: ['ignore', 'pipe', 'pipe'],
    env: process.env,
  });
  loginChild = child;
  let sawUrl = '';
  const onData = (chunk) => {
    const text = chunk.toString();
    appendLog(text);
    if (!sawUrl) {
      const url = extractUrl(text);
      if (url) { sawUrl = url; setLoginState('waiting', { authUrl: url }); }
    }
  };
  child.stdout.on('data', onData);
  child.stderr.on('data', onData);
  child.on('error', (err) => {
    if (loginChild === child) loginChild = null;
    setLoginState('error', { detail: `spawn failed: ${err.message}` });
  });
  child.on('close', (code) => {
    if (loginChild === child) loginChild = null;
    if (code === 0) {
      setLoginState('success', { detail: 'Login successful' });
    } else if (loginState === 'waiting') {
      const lastLine = logTail.split('\n').map((l) => l.trim()).filter(Boolean).pop() || '';
      setLoginState('error', { detail: `qoderclicn login exited with ${code}: ${lastLine}`.slice(0, 300) });
    }
    statusCache = null;
  });
  return { reused: false };
}

function runStatus() {
  if (statusCache && Date.now() - statusCache.at < STATUS_TTL_MS) return statusCache.promise;
  const promise = new Promise((resolve) => {
    const child = spawn(CLI, ['status', '-o', 'json'], {
      stdio: ['ignore', 'pipe', 'pipe'],
      env: process.env,
    });
    let out = '', err = '';
    const timer = setTimeout(() => {
      child.kill('SIGKILL');
      resolve({ ok: false, error: 'status timeout' });
    }, 15000);
    child.stdout.on('data', (d) => { out += d; });
    child.stderr.on('data', (d) => { err += d; });
    child.on('close', (code) => {
      clearTimeout(timer);
      if (code !== 0) {
        resolve({ ok: false, error: (err || `exit ${code}`).slice(0, 200) });
        return;
      }
      try {
        resolve({ ok: true, status: JSON.parse(out) });
      } catch (e) {
        resolve({ ok: false, error: `bad json: ${e.message}` });
      }
    });
    child.on('error', (e) => { clearTimeout(timer); resolve({ ok: false, error: e.message }); });
  });
  statusCache = { at: Date.now(), promise };
  return promise;
}

// qoderclicn 没有 logout 子命令；删除配置目录中的凭据文件。
// 凭据文件名以实际登录后落盘为准（见 deploy/qoder.Dockerfile 中的说明），
// 只删凭据/令牌类文件，保留 settings.json、installation_id 等非敏感配置。
const CREDENTIAL_BASENAMES = new Set([
  'auth.json',
  'credentials.json',
  'oauth_creds.json',
  'mcp-oauth-tokens.json',
]);
function logout() {
  let removed = [];
  let walkErr = '';
  const walk = (dir, depth) => {
    let entries;
    try { entries = fs.readdirSync(dir, { withFileTypes: true }); }
    catch (e) { walkErr = e.message; return; }
    for (const ent of entries) {
      const full = path.join(dir, ent.name);
      if (ent.isDirectory()) {
        if (depth < 3) walk(full, depth + 1);
      } else if (CREDENTIAL_BASENAMES.has(ent.name)) {
        try { fs.rmSync(full, { force: true }); removed.push(full); }
        catch (e) { walkErr = e.message; }
      }
    }
  };
  walk(CONFIG_DIR, 0);
  statusCache = null;
  // 清理可能残留的登录进程状态。
  if (loginChild) {
    try { loginChild.kill('SIGTERM'); } catch { /* ignore */ }
    loginChild = null;
  }
  setLoginState('idle', { authUrl: '', detail: '' });
  return { removed, error: walkErr };
}

function timingEqual(a, b) {
  const ba = Buffer.from(String(a));
  const bb = Buffer.from(String(b));
  if (ba.length !== bb.length) return false;
  return crypto.timingSafeEqual(ba, bb);
}

function send(res, code, body) {
  const data = JSON.stringify(body);
  res.writeHead(code, {
    'content-type': 'application/json; charset=utf-8',
    'content-length': Buffer.byteLength(data),
    'cache-control': 'no-store',
  });
  res.end(data);
}

const server = http.createServer((req, res) => {
  // 浏览器跨域请求必带 Origin；服务端到服务端调用不带。
  if (req.headers.origin) return send(res, 403, { error: 'browser origin forbidden' });
  // 自定义头：浏览器简单请求无法携带，触发预检后被无 CORS 响应阻断。
  if (req.headers['x-qoder-ctl'] !== '1') return send(res, 403, { error: 'control header required' });
  if (API_KEY) {
    const auth = req.headers.authorization || '';
    const expected = `Bearer ${API_KEY}`;
    if (!auth || auth.length !== expected.length || !timingEqual(auth, expected)) {
      return send(res, 401, { error: 'unauthorized' });
    }
  }
  const url = new URL(req.url, `http://${HOST}`);
  if (req.method === 'GET' && url.pathname === '/healthz') {
    return send(res, 200, { ok: true });
  }
  if (req.method === 'GET' && url.pathname === '/status') {
    return runStatus().then((r) => send(res, r.ok ? 200 : 502, r));
  }
  if (req.method === 'POST' && url.pathname === '/login/start') {
    const r = startLogin();
    return send(res, 200, {
      state: loginState,
      auth_url: authUrl,
      reused: r.reused,
      note: authUrl ? '' : '授权链接生成中，请轮询 GET /login',
    });
  }
  if (req.method === 'GET' && url.pathname === '/login') {
    return send(res, 200, {
      state: loginState,
      auth_url: authUrl,
      detail,
      finished_at: finishedAt || null,
    });
  }
  if (req.method === 'POST' && url.pathname === '/logout') {
    return send(res, 200, logout());
  }
  send(res, 404, { error: 'not found' });
});

server.listen(PORT, HOST, () => {
  console.log(`qoder-login-ctl listening on http://${HOST}:${PORT} (auth=${API_KEY ? 'bearer' : 'loopback-only'})`);
});

// 登录链接生成窗口通常 10 分钟；僵尸状态兜底：success/error 保留 10 分钟后回 idle。
setInterval(() => {
  if (!loginChild && (loginState === 'success' || loginState === 'error') &&
      finishedAt && Date.now() - finishedAt > 10 * 60 * 1000) {
    setLoginState('idle', { authUrl: '', detail: '' });
  }
}, 30 * 1000).unref();
