# --- build stage: тут компилится CCXT (нужно ~7GB RAM, есть в CI) ---
FROM golang:1.24-bookworm AS build
WORKDIR /probe
COPY go.mod .
COPY main.go .
# GOGC=30 + -p 1 снижают пик памяти компилятора (пакет-монолит CCXT).
ENV GOGC=30
RUN go mod tidy
RUN go build -p 1 -o /probe/ccxt-probe .

# --- runtime stage: тонкий, только бинарь + TLS-сертификаты ---
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /probe/ccxt-probe /usr/local/bin/ccxt-probe
ENTRYPOINT ["/usr/local/bin/ccxt-probe"]
