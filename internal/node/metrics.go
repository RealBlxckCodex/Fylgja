// Package node ist fylgja-node: Agent neben vLLM/Ollama auf GPU- oder lokalen Nodes (Spec 16.9).
package node

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/realblxckcodex/fylgja/internal/fleet"
)

// ParseNvidiaSMI parst `nvidia-smi --query-gpu=utilization.gpu,memory.used,memory.total,temperature.gpu,power.draw --format=csv,noheader,nounits`.
func ParseNvidiaSMI(out string) (m fleet.Metrics, gpus int) {
	var util float64
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.Split(line, ",")
		if len(f) < 5 {
			continue
		}
		num := func(i int) float64 {
			v, _ := strconv.ParseFloat(strings.TrimSpace(f[i]), 64)
			return v
		}
		gpus++
		util += num(0)
		m.VRAMUsedMB += int(num(1))
		m.VRAMTotalMB += int(num(2))
		m.TempC = max(m.TempC, num(3))
		m.PowerW += num(4)
	}
	if gpus > 0 {
		m.GPUUtil = util / float64(gpus) / 100
	}
	return m, gpus
}

// ParseVLLM liest relevante Werte aus dem Prometheus-Endpunkt von vLLM.
func ParseVLLM(text string) (running, waiting int, kv, prefixHit, genTokens float64) {
	var hits, queries float64
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "#") {
			continue
		}
		name, rest, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		if i := strings.IndexByte(name, '{'); i >= 0 {
			name = name[:i]
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			continue
		}
		v, err := strconv.ParseFloat(fields[len(fields)-1], 64)
		if err != nil {
			if i := strings.LastIndexByte(line, ' '); i > 0 {
				v, err = strconv.ParseFloat(line[i+1:], 64)
			}
			if err != nil {
				continue
			}
		}
		switch name {
		case "vllm:num_requests_running":
			running += int(v)
		case "vllm:num_requests_waiting":
			waiting += int(v)
		case "vllm:gpu_cache_usage_perc", "vllm:kv_cache_usage_perc":
			kv = max(kv, v)
		case "vllm:prefix_cache_hits_total", "vllm:gpu_prefix_cache_hits_total":
			hits += v
		case "vllm:prefix_cache_queries_total", "vllm:gpu_prefix_cache_queries_total":
			queries += v
		case "vllm:generation_tokens_total":
			genTokens += v
		}
	}
	if queries > 0 {
		prefixHit = hits / queries
	}
	return
}

// Collector sammelt Metriken (alle 2–5 s).
type Collector struct {
	VLLMMetricsURL string // http://127.0.0.1:8000/metrics
	HTTP           *http.Client

	mu       sync.Mutex
	lastTok  float64
	lastAt   time.Time
	GPUModel string
	GPUs     int
}

func (c *Collector) Collect(ctx context.Context) fleet.Metrics {
	var m fleet.Metrics
	if out, err := exec.CommandContext(ctx, "nvidia-smi", "--query-gpu=utilization.gpu,memory.used,memory.total,temperature.gpu,power.draw",
		"--format=csv,noheader,nounits").Output(); err == nil {
		m, c.GPUs = ParseNvidiaSMI(string(out))
		if c.GPUModel == "" {
			if n, err := exec.CommandContext(ctx, "nvidia-smi", "--query-gpu=name", "--format=csv,noheader").Output(); err == nil {
				c.GPUModel = strings.TrimSpace(strings.Split(string(n), "\n")[0])
			}
		}
	}
	if c.VLLMMetricsURL != "" {
		hc := c.HTTP
		if hc == nil {
			hc = &http.Client{Timeout: 3 * time.Second}
		}
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.VLLMMetricsURL, nil)
		if resp, err := hc.Do(req); err == nil {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
			resp.Body.Close()
			var tok float64
			m.RunningReqs, m.QueuedReqs, m.KVCacheUtil, m.PrefixHitRate, tok = ParseVLLM(string(b))
			c.mu.Lock()
			now := time.Now()
			if !c.lastAt.IsZero() && tok >= c.lastTok {
				m.TokensPerS = (tok - c.lastTok) / now.Sub(c.lastAt).Seconds()
			}
			c.lastTok, c.lastAt = tok, now
			c.mu.Unlock()
		}
	}
	return m
}
