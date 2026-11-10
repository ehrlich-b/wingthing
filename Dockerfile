FROM node:22-alpine AS web
WORKDIR /app/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.26.9-alpine AS build
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /app/web/dist web/dist
ARG REQUIRE_DOWNLOADS=0
RUN if [ "$REQUIRE_DOWNLOADS" = "1" ]; then nice -n 10 go run -p 2 ./cmd/check-downloads .ship-downloads; fi
RUN CGO_ENABLED=0 nice -n 10 go build -p 2 -o wt ./cmd/wt

FROM alpine:3.21
RUN apk add --no-cache ca-certificates sqlite
WORKDIR /app
COPY --from=build /app/wt .
COPY .ship-downloads/ /app/downloads/
ENV WT_DOWNLOAD_DIR=/app/downloads
ENV HOME=/data
EXPOSE 8080
# The standalone image defaults to the same loopback-only, no-login gateway as
# `wt serve` on a host. Fly's authenticated process command explicitly replaces
# the listener with :8080. Container users can opt into host networking (as the
# self-host docs show) or supply OAuth and an explicit public listener.
CMD ["sh", "-c", "umask 077 && mkdir -p /data/.wingthing && exec ./wt serve"]
