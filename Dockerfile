FROM golang:1.24 AS builder
WORKDIR /workspace
COPY . .
ENV GOPROXY=off GOSUMDB=off
RUN go build -o /gateway ./cmd/gateway

FROM alpine:3.19
RUN addgroup -S gateway && adduser -S gateway -G gateway
USER gateway
WORKDIR /app
COPY --from=builder /gateway /usr/local/bin/gateway
EXPOSE 8000
ENTRYPOINT ["/usr/local/bin/gateway"]
CMD ["-config", "/app/config.yaml"]
