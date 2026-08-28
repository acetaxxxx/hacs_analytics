FROM golang:1.25-alpine AS build

WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/analyticsd ./cmd/analyticsd

FROM alpine:3.22

RUN addgroup -S homekeeper && adduser -S -G homekeeper homekeeper
WORKDIR /app
COPY --from=build /out/analyticsd /app/analyticsd
RUN mkdir -p /data && chown -R homekeeper:homekeeper /data /app
USER homekeeper
ENV HOMEKEEPER_LISTEN_ADDR=:8080 \
    HOMEKEEPER_DB_PATH=/data/homekeeper.db
EXPOSE 8080
ENTRYPOINT ["/app/analyticsd"]
