# 07: HACS、Docker、Windows Release 與完整測試

**What to build:** 將兩個 runtime 整理成可實際部署與維護的 monorepo 產品，補齊 HACS 合規、Docker/Windows 操作文件、CI 與資源驗證。

**Blocked by:** 06: HA Report Consumer 與 Telegram

**Status:** implemented (host validation pending)

- [ ] HACS 只 package Home Assistant integration；Go sidecar 有獨立 module、Docker build、設定、health、更新與 Windows 操作文件。
- [ ] 補齊 HACS 所需 metadata、issue tracker、brand/icon 與 sidecar 安裝說明，不把 binary/token 放在 integration 目錄。
- [ ] Python/HACS validation 與 Go test/build/release checks 分開執行，並驗證 contract compatibility。
- [ ] 在約 1 GB RAM 的限制下完成 sidecar soak test，確認 SQLite、HTTP body、report DTO 與 Telegram rendering 有 bounded 行為。
- [ ] 補齊 migration、retention、restart recovery、secret redaction、LAN-only security 與 failure-mode 文件。
- [ ] 完成從 HA event 到 Telegram report 的 E2E fixture，並確認沒有寫入 Pi analytics database 或依賴 HA Recorder 私有 schema。
