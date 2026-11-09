FROM golang:1.24-bookworm AS build
RUN apt-get update && apt-get install -y --no-install-recommends libopus-dev libopusfile-dev pkg-config && rm -rf /var/lib/apt/lists/*
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 go build -trimpath -ldflags="-s -w" -o /out/ralsei .

FROM debian:bookworm-slim AS tts
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl && rm -rf /var/lib/apt/lists/*
RUN curl -fsSL https://github.com/rhasspy/piper/releases/download/2023.11.14-2/piper_linux_aarch64.tar.gz | tar xz -C /opt
RUN curl -fsSL -o /opt/piper/voice.onnx https://huggingface.co/rhasspy/piper-voices/resolve/main/en/en_US/lessac/medium/en_US-lessac-medium.onnx && curl -fsSL -o /opt/piper/voice.onnx.json https://huggingface.co/rhasspy/piper-voices/resolve/main/en/en_US/lessac/medium/en_US-lessac-medium.onnx.json

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends libopus0 libopusfile0 ca-certificates libstdc++6 libgomp1 libvirt-clients fonts-dejavu-core && rm -rf /var/lib/apt/lists/* && addgroup --gid 1000 ralsei && adduser --uid 1000 --gid 1000 --disabled-password --gecos "" ralsei
COPY --from=tts /opt/piper /opt/piper
COPY --from=build /out/ralsei /usr/local/bin/ralsei
COPY fonts /usr/local/share/ralsei
USER 1000:1000
WORKDIR /data
ENTRYPOINT ["/usr/local/bin/ralsei"]
