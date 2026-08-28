# 04: Deterministic Anomaly 與 Risk Evidence

**What to build:** 在不依賴 Gemini 的情況下，從 profile-aware rollup 產生可驗證的 anomaly/risk evidence、baseline、confidence 與通知等級。

**Blocked by:** 03: Entity Profiles 與 Daily Rollup

**Status:** ready-for-agent

- [ ] 建立 14 天 baseline 與 `insufficient_baseline` 語意；cold start 仍執行固定安全規則。
- [ ] 覆蓋 leak、smoke、gas、door/window、lock/security、abnormal temperature、long unavailable、abnormal energy 初始規則。
- [ ] 產生 high/medium/low risk、evidence IDs、coverage、data gap、sample count 與 confidence。
- [ ] high risk 可立即觸發告警，medium/low 進每日報告，並對重複風險套用 cooldown。
- [ ] 規則依 entity profile 與相容單位運作，不把 generic numeric entity 強行解讀成特定類型。
- [ ] Domain tests 覆蓋固定安全規則、baseline、cold start、data gap、cooldown 與證據可追溯性。
