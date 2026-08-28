# 02: Event Ingest 與 Heartbeat

**What to build:** 讓 HA integration 將 state change/snapshot 批次送到 sidecar，讓 sidecar 可靠去重、保存 raw records，並以 heartbeat 明確表示資料缺口。

**Blocked by:** 01: Sidecar 基礎、Health 與 Contract

**Status:** ready-for-agent

- [ ] HA integration 在 30 秒或 100 筆事件時送出 bounded event batch，不阻塞 HA event loop。
- [ ] Sidecar 驗證 batch 後以 transaction 寫入 raw event，重複 `event_id` 不會重複計算。
- [ ] HA integration 約每分鐘送 heartbeat，sidecar 產生 healthy 或 `data_gap` interval。
- [ ] sidecar 離線時 integration 保持 loaded/reconnecting，HTTP timeout/retry 有上限且不洩漏 resource。
- [ ] REST、HA integration 與 SQLite tests 覆蓋成功、duplicate、invalid、timeout、backpressure 與重啟後可辨識的資料品質狀態。
