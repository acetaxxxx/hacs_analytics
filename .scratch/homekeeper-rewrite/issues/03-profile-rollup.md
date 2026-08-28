# 03: Entity Profiles 與 Daily Rollup

**What to build:** 依 entity 類型與個別 override 解讀收集資料，建立 state/numeric/availability 統計與 local-date daily rollup，並維持明確 retention 與 profile version。

**Blocked by:** 02: Event Ingest 與 Heartbeat

**Status:** ready-for-agent

- [ ] 實作 numeric、binary、battery、energy、climate、contact/security、light/media、generic profile 的選擇優先順序。
- [ ] 預設全選 entity、explicit exclude 優先，且可逐 entity override profile、snapshot interval、threshold 與安全 metadata。
- [ ] Numeric entity 支援 state change 與預設五分鐘 snapshot；snapshot 不被算成 transition。
- [ ] 計算 state、unknown、unavailable intervals，以及 daily local calendar rollup；保留 UTC bounds 與 timezone。
- [ ] raw data 保留 30 天、rollup 保留 2 年；profile 修改只重算 retained raw window，歷史 rollup 保留原 profile version。
- [ ] 測試 profile precedence、unit mismatch、DST/local date、retention、recompute 與資料覆蓋率。
