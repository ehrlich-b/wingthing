FROM node:22-alpine AS web
WORKDIR /app/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.25-alpine AS build
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /app/web/dist web/dist
RUN CGO_ENABLED=0 go build -o wt ./cmd/wt

FROM alpine:3.21
RUN apk add --no-cache ca-certificates
WORKDIR /app
COPY --from=build /app/wt .
COPY spaces.yaml .
COPY spaces/cache/ spaces/cache/
EXPOSE 8080
CMD ["sh", "-c", "mkdir -p /data/.wingthing && exec ./wt serve --addr :8080"]
