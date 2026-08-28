# 05: Gemini Structured Report Lifecycle

**What to build:** 將 deterministic evidence 組成已脫敏的 bounded Gemini input，透過官方 Go SDK 產生結構化繁中報告，並具備 idempotent job、驗證與 bounded retry。

**Blocked by:** 04: Deterministic Anomaly 與 Risk Evidence

**Status:** ready-for-agent

- [ ] 直接使用 pinned `google.golang.org/genai` 呼叫 Gemini Developer API，API key 僅來自 sidecar secret。
- [ ] model 從 HA 設定，預設 Flash，允許支援的 Gemini model（包含 Pro）。
- [ ] Gemini input 只包含 pseudonymous IDs、safe labels、bounded aggregates、deterministic evidence 與 limitations，不含 raw event dump 或敏感 attributes。
- [ ] Gemini output 必須通過 versioned report JSON schema、enum、evidence reference 與 Traditional Chinese/English-key contract 驗證。
- [ ] report job 以 `report_date` idempotent，保存 config/profile snapshot 與 LLM attempt metadata。
- [ ] timeout、connection reset、429、5xx 依 15 分鐘、1 小時、4 小時、隔日重試；auth、invalid request、schema failure 不無限重試。
- [ ] SQLite report runs 在 restart 後能恢復 expired `ai_running` job，耗盡後標記 `ai_failed`。
- [ ] Fake Gemini tests 覆蓋 valid、invalid JSON、schema failure、missing evidence、timeout、rate limit、auth 與 retry classification。
