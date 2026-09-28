#!/usr/bin/env node
// 构建期补丁：放开 qoder-proxy 对 PAT 环境变量的硬性 401。
// 上游 runQoderCnCli()（cn 与国际站两个后端各一份）在 PAT 缺失时直接抛错，
// 导致 qoderclicn login 设备码登录得到的本地凭据永远无法被代理使用。
// 改为「有 PAT 用 PAT；没有则传空串，由 qoderclicn 自行回退到本地登录凭据」。
// 锚点必须精确匹配；上游升级导致结构或数量变化时构建失败，提醒人工复核。
'use strict';
const fs = require('fs');
const file = '/app/clean/qodercn-cli.js';
const s = fs.readFileSync(file, 'utf8');

// 匹配整段硬校验：const token = ...; if (!token) { throw new AppError(401, 'cli_token_missing', ...) }
const pattern = /const token = process\.env\[backend\.tokenEnvVar\];\s*if \(!token\) \{\s*throw new AppError\(\s*401,\s*'cli_token_missing',[\s\S]*?'authentication_error'\s*\);\s*\}/g;
const matches = s.match(pattern) || [];
if (matches.length !== 2) {
  console.error(`patch failed: expected 2 token guard blocks, found ${matches.length}`);
  process.exit(1);
}
const replacement = "// 本仓补丁：PAT 可选。缺失时传空串，由 qoderclicn 回退到 `qoderclicn login` 的本地凭据。\n  const token = process.env[backend.tokenEnvVar] || '';";
const next = s.replace(pattern, replacement);
if (next.includes('cli_token_missing')) {
  console.error('patch failed: cli_token_missing guard still present after replace');
  process.exit(1);
}
if ((next.match(/process\.env\[backend\.tokenEnvVar\] \|\| ''/g) || []).length !== 2) {
  console.error('patch failed: relaxed token line count mismatch');
  process.exit(1);
}
fs.writeFileSync(file, next, 'utf8');
console.log(`relax-token-guard patch applied (${matches.length} blocks)`);
