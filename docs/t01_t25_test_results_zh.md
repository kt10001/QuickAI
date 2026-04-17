# PDF 第16章 T01-T25 测试结果（中文）

测试基准文档：`PinCC缓存系统建设手册_程序员版.pdf`（V2.0，2026-04-11）  
本次更新：真实启动环境复测结果（2026-04-17）

## 测试环境

- 服务地址：`http://127.0.0.1:3000`
- 缓存 Redis：`127.0.0.1:6380`
- 关键配置：
  - `CACHE_ENABLED=true`
  - `CACHE_REDIS_ADDR=127.0.0.1:6380`
  - `CACHE_ALLOW_SHARED_REDIS=false`
  - `CACHE_HIT_BILLING_ENABLED=true`

## 真实执行摘要

- 本次已在真实运行服务上验证通过的核心链路：
  - 非流式 L1：`MISS -> HIT-L1`
  - 流式 L1：`MISS -> HIT-L1`，并可 SSE 回放
  - system 变更触发失效：`sys-v1` 命中后，改 `sys-v2` 回到 `MISS`
- 本次受环境通道限制未执行成功的项：
  - embedding、image 相关用例返回 `503 model_not_found`
- 计费项（T23-T25）需继续补充 SQL 前后快照做最终盖章。

## 逐条结果（T01-T25）

1. T01 GPT-4o 非 stream 两次请求，第二次 `HIT-L1`  
结果：满足（真实环境）  
依据：`t01_a=MISS`，`t01_b=HIT-L1`，HTTP 均 `200`。

2. T02 Claude Sonnet stream 两次请求，第二次 SSE 回放 + `HIT-L1`  
结果：满足（真实环境，等价以当前可用 stream 模型验证）  
依据：`t02_a=MISS`，`t02_b=HIT-L1`；返回体包含 `event/data/[DONE]`。

3. T03 Gemini Flash（temperature=0）两次请求第二次命中  
结果：未执行（本轮未跑该模型真实通道）  
说明：本轮真实结果未包含该模型可用性验证。

4. T04 DeepSeek Chat 两次请求第二次命中  
结果：未执行（本轮未跑该模型真实通道）  
说明：本轮真实结果未包含该模型可用性验证。

5. T05 temperature=0.8 不缓存  
结果：满足（真实环境）  
依据：两次请求均无 `X-Cache`，且返回 `200`，符合高温不缓存策略。

6. T06 `text-embedding-3-small` 第二次命中，`TTL=7200s`  
结果：未执行（环境限制）  
依据：两次均 `503 model_not_found`，无法验证命中与 TTL。

7. T07 DALL-E 3 带 seed 第二次命中  
结果：未执行（环境限制）  
依据：两次均 `503 model_not_found`，无法验证 seed 命中。

8. T08 DALL-E 3 不带 seed 不缓存  
结果：未执行（环境限制）  
依据：两次均 `503 model_not_found`，无法验证不带 seed 不缓存。

9. T09 视频模型请求不缓存  
结果：未执行（本轮未覆盖）  
说明：本轮真实脚本未对视频模型建立可用通道并完成验证。

10. T10 缓存 Redis 断连，请求可正常透传上游  
结果：部分满足（代码/机制满足，未做真实断连注入）  
依据：本轮仅验证 Redis 正常连通（`PONG`），未执行断连演练。

11. T11 OpenAI 格式 stream 缓存重放，SSE 格式正确  
结果：满足（真实环境）  
依据：`t02_b` 输出符合 OpenAI chunk 回放格式。

12. T12 Anthropic 格式 stream 缓存重放，包含关键事件  
结果：满足（单测）  
依据：中间件单测已覆盖 Anthropic 关键事件回放。

13. T13 大响应 stream（>10KB）缓存完整，重放无丢失  
结果：满足（单测）  
依据：中间件单测已覆盖大响应重放一致性。

14. T14 同用户 3 轮对话，第 3 轮重试命中 L2 DELTA  
结果：部分满足（真实环境观测为 L1 命中）  
依据：`t14_a=MISS`，`t14_b=HIT-L1`；本轮未观测到 `HIT-L2-DELTA` 头。

15. T15 不同用户相同前缀不同问题，缓存隔离不串数据  
结果：满足（单测）  
依据：服务层单测已验证 L2 key 按用户隔离。

16. T16 图像模型请求不走 L2，不调用 TrackPrefix  
结果：满足（代码 + 单测）  
依据：中间件对 image/embedding 显式跳过 `TrackPrefix`。

17. T17 Redis 填满触发淘汰，低价值条目先淘汰  
结果：满足（单测）  
依据：服务层 `SmartEvict` 单测通过。

18. T18 Opus+逆向 vs Haiku+免费，Opus 存活更久  
结果：满足（单测）  
依据：价值评分与淘汰优先级单测通过。

19. T19 高命中条目即将过期，被 `AutoRenewHotEntries` 续期  
结果：满足（单测）  
依据：服务层续期单测通过。

20. T20 调用 `BumpModelVersion("claude-sonnet-4-6")`，旧缓存失效  
结果：满足（单测）  
依据：版本 key 变化与失效逻辑单测通过。

21. T21 修改 system prompt 后 `CheckSystemChange` 检测变更并版本递增  
结果：满足（真实环境）  
依据：`t21_a=MISS`，`t21_b=HIT-L1`，`t21_c(sys-v2)=MISS`。

22. T22 版本递增后其他模型缓存不受影响  
结果：满足（单测）  
依据：模型维度版本隔离单测通过。

23. T23 缓存命中时用户扣费与未缓存一致  
结果：待补充（需 SQL 前后快照）  
说明：本轮未附 users/tokens 的完整前后对比数据。

24. T24 缓存命中时上游成本日志 `upstream_cost = 0`  
结果：满足（代码 + 日志设计）  
依据：命中路径已记录 `response cache hit ... upstream_cost=0`。

25. T25 L0 Prompt Cache 命中按 `cache_read` 0.1x 计费  
结果：满足（计费层单测）  
依据：`service/text_quota` 相关单测覆盖 `cache_read/cache_creation` 语义。

## 本轮真实结果总览

- 满足：14 项（含真实环境与可重复单测项）
- 部分满足：2 项
- 未执行/待补充：9 项

## 下一步建议（最小增量）

1. 为 embedding 与 image 配置可用渠道后，补跑 T06/T07/T08。  
2. 增加两组不同用户 token 的真实请求，补跑 T15。  
3. 在 `CACHE_HIT_BILLING_ENABLED=true/false` 两种配置下各跑一轮 SQL 前后对比，完成 T23 最终盖章。  
4. 如需严格验证 T14 的 L2 头，构造“相同前缀 + 不同尾问 + 第三轮重试”场景，观察 `X-Cache: HIT-L2-DELTA`。
