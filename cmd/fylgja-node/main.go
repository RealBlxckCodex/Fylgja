// Command fylgja-node läuft neben vLLM/Ollama auf einem GPU-Pod oder lokalen Server (Spec 16.9).
// Baut einen ausgehenden WSS-Tunnel zum Control Plane auf – es gibt keinen öffentlichen Inference-Port.
//
// Umgebung:
//
//	FYLGJA_ROUTER_URL   wss://fylgja.example.org/api/v1/node/tunnel
//	FYLGJA_NODE_ID      vom Fleet Manager vergeben
//	FYLGJA_NODE_TOKEN   einmaliges Node-Token
//	FYLGJA_UPSTREAM     127.0.0.1:8000 (OpenAI-kompatible API von vLLM/Ollama)
//	FYLGJA_DEPLOYMENTS  JSON: [{"model":"worker-default","served_model":"Qwen/Qwen3-32B","engine":"vllm","max_concurrency":16}]
//	FYLGJA_VLLM_METRICS http://127.0.0.1:8000/metrics (optional)
//	FYLGJA_REGION       z. B. EU-RO-1 (optional, sonst RUNPOD_DC_ID)
package main

import (
	"context"
	"encoding/json"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/realblxckcodex/fylgja/internal/fleet"
	"github.com/realblxckcodex/fylgja/internal/fleet/tunnel"
	"github.com/realblxckcodex/fylgja/internal/node"
	"github.com/realblxckcodex/fylgja/internal/platform/logging"
)

func main() {
	log := logging.New(os.Getenv("FYLGJA_LOG_LEVEL"), nil)
	need := func(k string) string {
		v := os.Getenv(k)
		if v == "" {
			log.Error("umgebungsvariable fehlt", "name", k)
			os.Exit(1)
		}
		return v
	}
	var deps []fleet.NodeDeployment
	if err := json.Unmarshal([]byte(need("FYLGJA_DEPLOYMENTS")), &deps); err != nil {
		log.Error("FYLGJA_DEPLOYMENTS ungültig", "err", err)
		os.Exit(1)
	}
	region := os.Getenv("FYLGJA_REGION")
	if region == "" {
		region = os.Getenv("RUNPOD_DC_ID")
	}
	col := &node.Collector{VLLMMetricsURL: os.Getenv("FYLGJA_VLLM_METRICS")}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	first := col.Collect(ctx)
	agent := &tunnel.Agent{
		URL: need("FYLGJA_ROUTER_URL"), NodeID: need("FYLGJA_NODE_ID"), Token: need("FYLGJA_NODE_TOKEN"), Upstream: need("FYLGJA_UPSTREAM"), Log: log,
		Interval: 5 * time.Second,
		Register: func() any {
			return fleet.Registration{GPUModel: col.GPUModel, GPUCount: col.GPUs, VRAMGB: first.VRAMTotalMB / 1024, Deployments: deps, Region: region}
		},
		Metrics: func() any {
			c, cancel := context.WithTimeout(ctx, 4*time.Second)
			defer cancel()
			return col.Collect(c)
		},
	}
	_ = os.Unsetenv("FYLGJA_NODE_TOKEN")
	if err := agent.Run(ctx); err != nil && ctx.Err() == nil {
		log.Error("tunnel", "err", err)
		os.Exit(1)
	}
}
