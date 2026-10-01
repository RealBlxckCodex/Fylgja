# Aurora (github.com/RealBlxckCodex/Aurora) aus dem Quelltext. Nicht gebaut oder getestet.
# Die ONNX- und Whisper-Backends brauchen je nach Build-Tags zusätzliche Bibliotheken (ONNX Runtime, espeak-ng,
# whisper.cpp); die Aurora-README beschreibt das. Passe dieses Image an deine Backend-Auswahl an.
FROM golang:1.24-bookworm AS build
ARG AURORA_REF=main
RUN git clone --depth 1 --branch ${AURORA_REF} https://github.com/RealBlxckCodex/Aurora /src
WORKDIR /src
RUN go build -o /out/aurora ./cmd/aurora

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends espeak-ng ca-certificates ffmpeg && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/aurora /usr/local/bin/aurora
VOLUME /var/aurora/models
EXPOSE 11435
ENTRYPOINT ["aurora", "serve", "--host", "0.0.0.0", "--port", "11435", "--cpu-only"]
