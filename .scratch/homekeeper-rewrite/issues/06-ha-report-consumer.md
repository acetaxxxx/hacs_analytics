# 06: HA Report Consumer 與 Telegram

**What to build:** 讓 Home Assistant 使用 sidecar 的 report job/result API，將完整 report 轉成 HA sensor、manual report 與既有 Telegram notification，並保持 sidecar 離線時可恢復。

**Blocked by:** 05: Gemini Structured Report Lifecycle

**Status:** ready-for-agent

- [ ] HA integration 能排程一個 active report time（預設 08:00，可選 12:00、18:00、22:00），請求前一個完整 local calendar day。
- [ ] 支援指定 `YYYY-MM-DD` 的 manual report，並與排程共用 report-date idempotency。
- [ ] 以 bounded polling 取得 report status/result，不重複觸發 Gemini，不把 sidecar downtime 變成 HA setup failure。
- [ ] Sensor 提供 compact view，完整 JSON 仍可透過 integration/API 取得。
- [ ] 使用 HA 既有 Telegram service 發送繁體中文報告，長訊息安全切割，並呈現 `data_quality`、`confidence`、`ai_status`。
- [ ] Options reload 重建 client；unload 取消 polling/retry、關閉 HTTP resource；測試覆蓋通知、輪詢、degraded 與 cleanup。
