# 构建阶段
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/updater ./cmd/updater

# 运行阶段：包含 docker CLI 与 Compose 插件（Alpine 包名）
FROM alpine:3.19
RUN apk add --no-cache ca-certificates docker-cli docker-cli-compose
COPY --from=build /out/updater /usr/local/bin/updater
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/updater"]
