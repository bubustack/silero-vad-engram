# 🎛️ Silero VAD Engram

This engram wraps [silero-vad-go](https://github.com/streamer45/silero-vad-go) to provide
ONNX-based voice-activity detection for the bobrapet realtime pipeline. It consumes
`speech.audio.v1` payloads emitted by ingress engrams and produces
`speech.vad.active` packets that contain utterance-sized PCM chunks along with
sequence numbers, durations, and storage references when needed. Consumers listen for
the standard Tractatus stream types (`speech.vad.*`), so they can swap in any VAD
provider without bespoke routing.

## 🌟 Highlights

- Uses Silero's CPU-friendly ONNX model for robust VAD across multiple providers.
- Pluggable frame sizing, silence tail, and amplitude thresholds with per-participant
  buffering identical to the legacy WebRTC-based detector.
- Optional WAV wrapping or raw PCM output plus automatic offload to shared storage
  when turns exceed the inline byte budget.
- Designed for the transport contracts (`speech.audio.v1` inputs) used across
  providers, and emits the shared `speech.vad.*` stream types so it can plug into any
  workflow that exchanges PCM via Tractatus.

## 🚀 Quick Start

```bash
make lint
go test ./...
make docker-build
```

Apply `Engram.yaml`, mount the Silero model and ONNX Runtime libraries, then
wire the engram into a realtime Story after your ingress step.

## ⚙️ Configuration (`Engram.spec.with`)

| Field | Default | Description |
|-------|---------|-------------|
| `modelPath` | `/models/silero_vad.onnx` | Path to the Silero ONNX file. Mount the model via a volume or baked image layer. |
| `detectorSampleRateHz` | `16000` | Sample rate fed into the Silero model (8 kHz or 16 kHz). |
| `threshold` | `0.5` | Probability threshold emitted by Silero to mark frames as speech. |
| `frameDurationMs` | `32` | PCM frame duration processed before each VAD decision. |
| `minSilenceDurationMs` | `400` | Trailing silence required to finalize a turn. |
| `minDurationMs` | `200` | Minimum amount of voiced audio before emitting a turn. |
| `maxDurationMs` | `12000` | Hard cap to avoid infinitely growing buffers. |
| `outputEncoding` | `wav` | `wav` wraps PCM in a RIFF header, `pcm` keeps raw PCM16. |
| `inlineAudioLimit` | `65536` | Inline byte budget before turn audio is offloaded via the shared storage manager. |
| `coalesceDurationMs` | `0` | Optional merge window that combines adjacent turns for the same participant before emission. |
| `coalesceMaxDurationMs` | `30000` | Maximum duration for a coalesced turn before forced emission. |

## 📦 Model Requirements

Silero requires the ONNX Runtime shared libraries. The provided Dockerfile downloads
`onnxruntime-linux-x64-1.18.1` during the build stage and copies the shared objects into
the final image. If you use a custom base image, ensure the following environment
variables point to the directory that contains `libonnxruntime.so`:

```
LD_LIBRARY_PATH=/opt/onnxruntime/lib
LIBRARY_PATH=/opt/onnxruntime/lib
C_INCLUDE_PATH=/opt/onnxruntime/include
```

The ONNX model itself is not embedded; mount it into the container filesystem and set
`modelPath` accordingly (for example by referencing a ConfigMap, PVC, or baked image
layer with the Silero checkpoint).

## 📥 Inputs

- **Inputs**: `type=speech.audio.v1`. The `audio.data` field must contain
  base64-encoded PCM16 data.

## 📤 Outputs

- **Outputs**: `type=speech.vad.active` with `audio.data` holding either raw PCM
  or WAV data depending on `outputEncoding`. When offloading kicks in, the `audio.storage`
  reference contains the blob path and matching content type (`audio/wav` for WAV output,
  `audio/L16` for raw PCM) instead of inline bytes.

## 🔄 Streaming Mode

| Stream type | Description |
|-------------|-------------|
| `speech.vad.active` | Emitted for every detected speech turn. Includes buffered audio, duration, and sequence metadata. |

The engram also tags metadata with `provider=silero`, letting downstream stories mix
and match VAD engines without brittle string comparisons.

## 🧪 Local Development

- `make lint` – Run the shared lint and static-analysis checks.
- `go test ./...` – Run the Silero/VAD test suite in an environment with ONNX Runtime headers installed.
- `make docker-build` – Build the engram image for local clusters.
- Set `BUBU_DEBUG=true` to log sanitized input packet summaries and emitted turn metadata without dumping raw PCM into the logs.

## 🔄 Runtime Notes

The engram runs as a streaming Deployment. Provide a read-only volume for
`/models` (or whichever path you configure) so the ONNX file is available before
startup. The default container exposes gRPC on port `50051` like other streaming
engrams.

### 🏗️ Building multi-architecture images

The Dockerfile now honors BuildKit's `TARGETPLATFORM`, so running:

```bash
docker buildx build --platform linux/amd64,linux/arm64 -t ghcr.io/bubustack/silero-vad-engram:latest --push .
```

publishes matching images for both architectures. When building without BuildKit,
pass `--build-arg TARGETARCH=$(uname -m)` to ensure the binary matches the base
image (`TARGETARCH=arm64` on Apple Silicon, `amd64` on x86 hosts).

## 🤝 Community & Support

- [Contributing](./CONTRIBUTING.md)
- [Support](./SUPPORT.md)
- [Security Policy](./SECURITY.md)
- [Code of Conduct](./CODE_OF_CONDUCT.md)
- [Discord](https://discord.gg/dysrB7D8H6)


## 📄 License

Copyright 2025 BubuStack.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
