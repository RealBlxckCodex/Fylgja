# fylgja-node: vLLM + Node-Agent (Spec 16.8). Kein öffentlicher Inference-Port – nur ausgehender Tunnel.
FROM golang:1.26-alpine AS build
WORKDIR /src
ENV CGO_ENABLED=0
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN mkdir -p web/dist && touch web/dist/.keep && go build -ldflags "-s -w" -o /out/fylgja-node ./cmd/fylgja-node

FROM vllm/vllm-openai:latest
COPY --from=build /out/fylgja-node /usr/local/bin/fylgja-node
COPY deploy/node/entrypoint.sh /usr/local/bin/fylgja-entrypoint.sh
RUN chmod 0755 /usr/local/bin/fylgja-entrypoint.sh
ENTRYPOINT ["/usr/local/bin/fylgja-entrypoint.sh"]
