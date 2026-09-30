# fylgja-computer:base – Sandbox einer Fylgja (Spec 13.3): Browser, Shell, Desktop, Live-View.
FROM golang:1.26-alpine AS build
WORKDIR /src
ENV CGO_ENABLED=0
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN mkdir -p web/dist && touch web/dist/.keep && go build -ldflags "-s -w" -o /out/computerd ./cmd/computerd

FROM mcr.microsoft.com/playwright:v1.55.0-noble
ENV DEBIAN_FRONTEND=noninteractive DISPLAY=:1 HOME=/home/dot
RUN apt-get update && apt-get install -y --no-install-recommends \
      xvfb x11vnc fluxbox novnc websockify xdotool git python3 python3-pip curl ca-certificates jq unzip \
      poppler-utils pandoc \
    && rm -rf /var/lib/apt/lists/* \
    && npm install -g @playwright/mcp@latest \
    && (userdel -r pwuser 2>/dev/null || true) \
    && useradd -m -u 1000 -s /bin/bash dot
COPY --from=build /out/computerd /usr/local/bin/computerd
COPY deploy/computer/entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod 0755 /usr/local/bin/entrypoint.sh
USER 1000:1000
WORKDIR /home/dot
EXPOSE 7070 6080
ENV COMPUTERD_BROWSER_CMD="npx @playwright/mcp --browser chromium --user-data-dir /home/dot/.browser"
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
