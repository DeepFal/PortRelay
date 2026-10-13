# PortRelay 边缘网关（服务端）镜像
#   docker build -t portrelay-server .
#   docker run -d --network host -e PORTRELAY_TOKEN=... -v portrelay:/var/lib/portrelay portrelay-server
# 请使用 host 网络：白名单依赖访问者的真实来源 IP，端口映射可能把它变成网桥地址。
FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
        -o /out/portrelay-server ./cmd/portrelay-server \
    && mkdir -p /out/state

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/portrelay-server /usr/local/bin/portrelay-server
COPY --from=build --chown=65532:65532 /out/state /var/lib/portrelay
VOLUME ["/var/lib/portrelay"]
EXPOSE 33000-34000
ENTRYPOINT ["/usr/local/bin/portrelay-server"]
CMD ["-listen", ":33000", "-port-min", "33000", "-port-max", "34000", "-state", "/var/lib/portrelay/leases.json"]
