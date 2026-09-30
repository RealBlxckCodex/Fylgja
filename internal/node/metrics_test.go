package node

import "testing"

func TestParsers(t *testing.T) {
	m, n := ParseNvidiaSMI("78, 61000, 81559, 64, 310.5\n50, 1000, 81559, 55, 100\n")
	if n != 2 || m.GPUUtil != 0.64 || m.VRAMUsedMB != 62000 || m.TempC != 64 {
		t.Fatalf("%+v %d", m, n)
	}
	run, wait, kv, hit, tok := ParseVLLM(`# HELP x
vllm:num_requests_running{model_name="qwen"} 3.0
vllm:num_requests_waiting{model_name="qwen"} 2.0
vllm:gpu_cache_usage_perc{model_name="qwen"} 0.42
vllm:prefix_cache_hits_total{model_name="qwen"} 30
vllm:prefix_cache_queries_total{model_name="qwen"} 60
vllm:generation_tokens_total{model_name="qwen"} 12345
`)
	if run != 3 || wait != 2 || kv != 0.42 || hit != 0.5 || tok != 12345 {
		t.Fatalf("%d %d %f %f %f", run, wait, kv, hit, tok)
	}
}
