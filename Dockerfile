# 构建阶段
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/updater ./cmd/updater

# 运行阶段：使用官方 Docker CLI 镜像，避免 Alpine 仓库中的旧版 Compose 插件
FROM docker:29-cli
COPY --from=build /out/updater /usr/local/bin/updater
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/updater"]
