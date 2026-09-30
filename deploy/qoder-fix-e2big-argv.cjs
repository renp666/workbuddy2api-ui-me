#!/usr/bin/env node
// 构建期补丁：修复 Linux 容器下 qoder-proxy 的 spawn E2BIG。
// 上游 fixLongAppendSystemPrompt() 只在 win32 平台把超长的 --append-system-prompt
// 挪进 attachment 文件；Linux 分支被 `process.platform !== 'win32'` 直接跳过。
// 但 Linux 内核 MAX_ARG_STRLEN=131072 字节限制单个 argv 参数长度，Agent 客户端
// （pi 等）动辄 >128KB 的 system prompt 会让 spawn 立即抛 E2BIG（HTTP 500）。
// 本补丁给 Linux 加同构处理：system prompt 超阈值时并入 attachment、从 argv 移除。
// 锚点必须精确匹配且各出现一次；上游结构变化时构建失败，提醒人工复核。
'use strict';
const fs = require('fs');
const file = '/app/clean/qodercn-cli.js';
const s = fs.readFileSync(file, 'utf8');

// 1) 去掉 win32 早退，让所有平台都进入超长判定。
const guardOld = "  if (process.platform !== 'win32' || !attachmentPath) return args;";
const guardNew = "  if (!attachmentPath) return args;";
if (s.split(guardOld).length - 1 !== 1) {
  console.error('patch failed: expected exactly 1 win32 guard line');
  process.exit(1);
}

// 2) 把总长度阈值判定改为平台分支：win32 维持原总长度逻辑，Linux 按单参数字节数判定。
const lenOld =
  "  // Rough estimate: include command name and a safety margin\n" +
  "  const totalLength = (command?.length || 10) + args.reduce((acc, s) => acc + s.length + 1, 0);\n" +
  "  const isCmdShell = /cmd(\\.exe)?$/i.test(command || '');\n" +
  "  const limit = isCmdShell ? 7500 : 30000;\n" +
  "  if (totalLength < limit) return args;";
const lenNew =
  "  // 本仓补丁：win32 按命令行总长度判定；Linux 单参数上限 MAX_ARG_STRLEN=131072 字节，\n" +
  "  // 按 --append-system-prompt 值的字节数判定，超阈值则并入 attachment 避免 spawn E2BIG。\n" +
  "  if (process.platform === 'win32') {\n" +
  "    const totalLength = (command?.length || 10) + args.reduce((acc, s) => acc + s.length + 1, 0);\n" +
  "    const isCmdShell = /cmd(\\.exe)?$/i.test(command || '');\n" +
  "    const limit = isCmdShell ? 7500 : 30000;\n" +
  "    if (totalLength < limit) return args;\n" +
  "  } else if (Buffer.byteLength(systemPrompt, 'utf8') < 120000) {\n" +
  "    return args;\n" +
  "  }";
if (s.split(lenOld).length - 1 !== 1) {
  console.error('patch failed: expected exactly 1 length-limit block');
  process.exit(1);
}

let next = s.replace(guardOld, guardNew);
next = next.replace(lenOld, lenNew);

// 校验：补丁后不应再有无条件 win32 早退，且 Linux 分支存在。
if (next.includes("if (process.platform !== 'win32' || !attachmentPath) return args;")) {
  console.error('patch failed: win32 guard still present after replace');
  process.exit(1);
}
if (!next.includes("Buffer.byteLength(systemPrompt, 'utf8') < 120000")) {
  console.error('patch failed: linux byte-limit branch missing');
  process.exit(1);
}

fs.writeFileSync(file, next, 'utf8');
console.log('fix-e2big-argv patch applied');
