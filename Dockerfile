# syntax=docker/dockerfile:1.7

# --- Build Stage ---
FROM --platform=$BUILDPLATFORM golang:1.26-bookworm AS builder

ARG ONNXRUNTIME_VERSION=1.18.1
ARG TARGETOS
ARG TARGETARCH

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    curl \
    build-essential \
 && rm -rf /var/lib/apt/lists/*

RUN case "${TARGETARCH}" in \
        amd64) ORT_PACKAGE="onnxruntime-linux-x64-${ONNXRUNTIME_VERSION}";; \
        arm64) ORT_PACKAGE="onnxruntime-linux-aarch64-${ONNXRUNTIME_VERSION}";; \
        *) echo "Unsupported TARGETARCH ${TARGETARCH}" && exit 1;; \
    esac \
 && curl -L -o /tmp/onnxruntime.tgz https://github.com/microsoft/onnxruntime/releases/download/v${ONNXRUNTIME_VERSION}/${ORT_PACKAGE}.tgz \
 && tar -C /opt -xzf /tmp/onnxruntime.tgz \
 && mv /opt/${ORT_PACKAGE} /opt/onnxruntime \
 && rm /tmp/onnxruntime.tgz

ENV LD_LIBRARY_PATH=/opt/onnxruntime/lib
ENV LIBRARY_PATH=/opt/onnxruntime/lib
ENV C_INCLUDE_PATH=/opt/onnxruntime/include
ENV CGO_LDFLAGS="-L/opt/onnxruntime/lib"
ENV CGO_CFLAGS="-I/opt/onnxruntime/include"

WORKDIR /src
COPY . .

WORKDIR /src
RUN go mod download

RUN set -eux; \
    : "${TARGETARCH:?TARGETARCH not set}"; \
    : "${TARGETOS:=linux}"; \
    CC=gcc; \
    case "${TARGETARCH}" in \
        amd64) \
            if [ "$(dpkg --print-architecture)" != "amd64" ]; then \
                apt-get update; \
                apt-get install -y --no-install-recommends gcc-x86-64-linux-gnu libc6-dev-amd64-cross; \
                CC=x86_64-linux-gnu-gcc; \
            fi \
            ;; \
        arm64) \
            if [ "$(dpkg --print-architecture)" != "arm64" ]; then \
                apt-get update; \
                apt-get install -y --no-install-recommends gcc-aarch64-linux-gnu libc6-dev-arm64-cross; \
                CC=aarch64-linux-gnu-gcc; \
            fi \
            ;; \
    esac; \
    CGO_ENABLED=1 GOOS=${TARGETOS} GOARCH=${TARGETARCH} CC=${CC} go build -o silero-vad-engram .

# --- Final Stage ---
FROM --platform=$TARGETPLATFORM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl \
 && rm -rf /var/lib/apt/lists/*

ARG ONNXRUNTIME_VERSION=1.18.1
ARG TARGETARCH
ARG SILERO_VAD_MODEL_URL=https://raw.githubusercontent.com/snakers4/silero-vad/master/src/silero_vad/data/silero_vad.onnx
COPY --from=builder /opt/onnxruntime /opt/onnxruntime
ENV LD_LIBRARY_PATH=/opt/onnxruntime/lib
ENV CGO_LDFLAGS="-L/opt/onnxruntime/lib"
ENV CGO_CFLAGS="-I/opt/onnxruntime/include"

COPY --from=builder /src/silero-vad-engram /silero-vad-engram
RUN set -eux; \
    mkdir -p /models; \
    curl -L -o /models/silero_vad.onnx "${SILERO_VAD_MODEL_URL}"
ENTRYPOINT ["/silero-vad-engram"]
