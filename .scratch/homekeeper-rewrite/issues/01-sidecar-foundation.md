# 01: Sidecar 基礎、Health 與 Contract

**What to build:** 建立可部署的 Go analytics sidecar 基礎，讓 Home Assistant 能以版本化、受保護的 REST seam 確認 sidecar 狀態與相容性。

**Blocked by:** None (can start immediately)

**Status:** ready-for-agent

- [ ] Go sidecar 可在 Docker 中啟動，並以清楚的 liveness/readiness 狀態回應 health request。
- [ ] 建立 SQLite migration 基礎與 service 啟動檢查，但尚不實作 analytics domain。
- [ ] REST API 驗證 shared Bearer token、request ID、contract major version、body limit，並回傳穩定 error code。
- [ ] OpenAPI/JSON Schema contract 與實際 health endpoint 一致，具備 contract tests。
- [ ] HA integration 能設定 sidecar URL、shared token、timeout，且 sidecar 暫時離線時保持 degraded 而非讓 HA setup 失敗。
