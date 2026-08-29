FROM node:22-alpine AS web
WORKDIR /app/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.26.6-alpine AS build
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /app/web/dist web/dist
RUN CGO_ENABLED=0 go build -o wt ./cmd/wt

FROM alpine:3.21
RUN apk add --no-cache ca-certificates sqlite
WORKDIR /app
COPY --from=build /app/wt .
COPY hero.mp4 .
ENV WT_HERO_VIDEO=/app/hero.mp4
ENV HOME=/data
EXPOSE 8080
# The standalone image defaults to the same loopback-only, no-login gateway as
# `wt serve` on a host. Fly's authenticated process command explicitly replaces
# the listener with :8080. Container users can opt into host networking (as the
# self-host docs show) or supply OAuth and an explicit public listener.
CMD ["sh", "-c", "umask 077 && mkdir -p /data/.wingthing && exec ./wt serve"]
