# 本机镜像代际与一次删除的留档

2026-10-06 采集，判据全部是 `docker image ls --format '{{.Repository}}:{{.Tag}}\t{{.ID}}'` 的直接读数。

## 删除前登记

`registry.cn-hangzhou.aliyuncs.com/cateyes/go:wb2api-{core,webui}-1791196530`
一对镜像的 ID：

| 镜像 | ID | 构建时间 |
| --- | --- | --- |
| `wb2api-core-1791196530` | `82d6d04bc507` | 2026-10-05（约 23 小时前） |
| `wb2api-webui-1791196530` | `c03b35cccbf4` | 2026-10-05（约 22 小时前） |

- 这一对是 **2026-10-05 通过隔离容器验收那一版的实物**。
- 它**从未进入注册表**：`docker manifest inspect registry.cn-hangzhou.aliyuncs.com/cateyes/go:wb2api-core-1791196530`
  返回 `no such manifest`（同一条命令对 `1789550372` 返回 rc=0 和真实 OCI index，说明注册表可达、判据活着）。
  发布腿在 2026-10-05 被取消，此后不再恢复。
- 因此本次 `docker rmi` 之后，**没有任何第二份实物能复现那次验收跑的字节**，只能从 `HEAD` 重新构建。
  登记 ID 的作用是把「哪天验的是哪一版」这件事留在仓库里，而不是留在盘上。
- 删除不影响任何在跑的东西：`docker ps -a --format '{{.Image}}'` 里没有一项引用它，也没有其他标签指向同一 ID。

## 删除后本机剩下的代际

| 标签 | ID | 角色 |
| --- | --- | --- |
| `workbuddy2api-core:local` / `workbuddy2api-console:local` | `64d8de5011c7` / `45462b8ddacd` | 本机入口在跑的最新构建，上下文 `git archive HEAD`（基点 `338ee4c`） |
| `workbuddy2api-core:local-prev` / `workbuddy2api-console:local-prev` | `7f14c11053c9` / `9d9023fc3f15` | 一次回退用的上一代 |
| `…cateyes/go:wb2api-core-1789550372` / `…webui-1789550372` | `518fe487b4c9` / `b1b6221e82c0` | **不清理**：`docker-compose.yml` 第 3、17 行按名字钉着它，删了本机生产入口就离线起不来；注册表有副本 |

清理尺度上的这条豁免写进 `AGENTS.md` 的「构建与发布」一节：凡被入库 Compose 按名字钉住的镜像不进清理范围。
