FROM golang:1.24 AS builder
WORKDIR /workspace
COPY . .

# Allow dependencies to be downloaded using the default Go module proxy.
# This keeps the build working in environments where external access is
# permitted, while still letting callers override the proxy via build args.
ARG GOPROXY=https://proxy.golang.org,direct
ARG GOSUMDB=sum.golang.org
ENV GOPROXY=${GOPROXY} \
    GOSUMDB=${GOSUMDB}

RUN go build -o /gateway ./cmd/gateway

FROM alpine:3.19
RUN addgroup -S gateway && adduser -S gateway -G gateway
USER gateway
WORKDIR /app
COPY --from=builder /gateway /usr/local/bin/gateway
EXPOSE 8000
ENTRYPOINT ["/usr/local/bin/gateway"]
CMD ["-config", "/app/config.yaml"]
