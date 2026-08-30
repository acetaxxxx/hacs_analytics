# Windows host deployment

The old Windows computer is an external analytics host. Home Assistant and
HACS do not install or supervise this process.

## Docker Desktop

1. Install Docker Desktop with Linux containers enabled.
2. Pull a published release image, or build the sidecar from
   `services/analyticsd`:

   ```powershell
   docker pull ghcr.io/acetaxxxx/homekeeper-analyticsd:v0.1.0
   ```

   ```powershell
   docker build -t homekeeper-analyticsd .
   ```

3. Start it with a host directory and LAN-only port publishing. Create
   `C:\homekeeper\data` first so the SQLite file is easy to inspect:

   ```powershell
   docker run -d --name homekeeper-analyticsd `
     -p 192.168.1.20:8080:8080 `
     -e HOMEKEEPER_SHARED_TOKEN=$env:HOMEKEEPER_SHARED_TOKEN `
     -e GEMINI_API_KEY=$env:GEMINI_API_KEY `
     -e HOMEKEEPER_TIMEZONE=Asia/Taipei `
     --mount type=bind,source=C:\homekeeper\data,target=/data `
     ghcr.io/acetaxxxx/homekeeper-analyticsd:v0.1.0
   ```

Keep the API port on the trusted home LAN. Do not commit the shared token or
Gemini key. The SQLite database is `C:\homekeeper\data\homekeeper.db` on
the host; backups are not part of the first release.

For a native Windows service, build `GOOS=windows GOARCH=amd64` from the Go
module and run the resulting binary under a dedicated low-privilege account.
The same environment variables and health endpoint apply.
