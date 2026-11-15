FROM golang:1.24.0-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 go build -trimpath -o /out/bot ./cmd/bot && CGO_ENABLED=1 go build -trimpath -o /out/content ./cmd/content
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/* && useradd -u 10001 -m amber && mkdir /data && chown amber:amber /data
WORKDIR /app
COPY --from=build /out/ /app/bin/
COPY content/test.json /app/content/test.json
USER amber
ENV DB_PATH=/data/amber.db CONTENT_FILE=/app/content/test.json
ENTRYPOINT ["/app/bin/bot"]
