// Command fyctl ist die CLI für Fylgja (Admin, Debug, Export, Replay).
//
// Anmeldung per API-Token: FYCTL_SERVER=https://fylgja.example.org FYCTL_TOKEN=fyt_… fyctl status
// Offline-Prüfung der Audit-Kette direkt gegen die DB: fyctl audit verify --db postgres://…
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/realblxckcodex/fylgja/internal/audit"
	"github.com/realblxckcodex/fylgja/internal/store"
)

type client struct {
	base, token string
	hc          *http.Client
}

func newClient() *client {
	c := &client{base: strings.TrimRight(os.Getenv("FYCTL_SERVER"), "/"), token: os.Getenv("FYCTL_TOKEN"), hc: &http.Client{Timeout: 60 * time.Second}}
	if c.base == "" || c.token == "" {
		if b, err := os.ReadFile(cfgFile()); err == nil {
			var f struct{ Server, Token string }
			_ = json.Unmarshal(b, &f)
			c.base, c.token = strings.TrimRight(f.Server, "/"), f.Token
		}
	}
	if c.base == "" || c.token == "" {
		die("nicht angemeldet: FYCTL_SERVER/FYCTL_TOKEN setzen oder `fyctl login <server> <token>`")
	}
	return c
}

func cfgFile() string {
	d, _ := os.UserConfigDir()
	return filepath.Join(d, "fyctl.json")
}

func (c *client) do(method, path string, body any, out any) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.base+"/api/v1"+path, rd)
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		die(err.Error())
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		die(fmt.Sprintf("%s %s: %d %s", method, path, resp.StatusCode, strings.TrimSpace(string(b))))
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			die("antwort: " + err.Error())
		}
	}
}

func die(msg string) {
	fmt.Fprintln(os.Stderr, "fyctl:", msg)
	os.Exit(1)
}

func table(header string, rows [][]string) {
	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, header)
	for _, r := range rows {
		fmt.Fprintln(w, strings.Join(r, "\t"))
	}
	w.Flush()
}

func eur(v any) string {
	f, _ := v.(float64)
	return fmt.Sprintf("%.2f €", f/1e6)
}

const help = `fyctl – Fylgja-CLI

  fyctl login <server> <api-token>
  fyctl status                         Überblick (Fylgjur, Freigaben, Kosten)
  fyctl dots                           Fylgjur auflisten
  fyctl chat <dot-id> <text…>          Nachricht senden und Antwort abwarten
  fyctl runs <dot-id>                  letzte Runs
  fyctl run <run-id>                   Journal eines Runs (Replay)
  fyctl approvals                      offene Freigaben
  fyctl approve <id> | deny <id> [grund]
  fyctl audit verify [--db URL]        Hashchain prüfen (API oder direkt gegen die DB)
  fyctl fleet                          Flotte: Nodes, Deployments, Queues
  fyctl fleet add-node <name> [region] lokalen Inference-Node registrieren
  fyctl fleet drain|terminate <node-id>
  fyctl memory export <dot-id> <ordner>
  fyctl pause-all [--hard] | resume-all
`

func main() {
	if len(os.Args) < 2 {
		fmt.Print(help)
		return
	}
	args := os.Args[2:]
	switch os.Args[1] {
	case "login":
		if len(args) != 2 {
			die("nutzung: fyctl login <server> <token>")
		}
		_ = os.MkdirAll(filepath.Dir(cfgFile()), 0o700)
		b, _ := json.Marshal(map[string]string{"server": args[0], "token": args[1]})
		if err := os.WriteFile(cfgFile(), b, 0o600); err != nil {
			die(err.Error())
		}
		var me map[string]any
		newClient().do("GET", "/auth/me", nil, &me)
		fmt.Println("angemeldet als", me["principal"].(map[string]any)["email"])
	case "status":
		c := newClient()
		var dots []map[string]any
		c.do("GET", "/dots", nil, &dots)
		var rows [][]string
		for _, d := range dots {
			dd := d["dot"].(map[string]any)
			rows = append(rows, []string{fmt.Sprint(dd["name"]), fmt.Sprint(dd["status"]), fmt.Sprintf("L%v", dd["autonomy_level"]),
				fmt.Sprint(d["running"]), fmt.Sprint(d["pending_approvals"]), eur(d["cost_today_micro_eur"])})
		}
		table("FYLGJA\tSTATUS\tAUTONOMIE\tLÄUFE\tFREIGABEN\tKOSTEN HEUTE", rows)
		var fo map[string]any
		c.do("GET", "/fleet/overview", nil, &fo)
		s := fo["summary"].(map[string]any)
		fmt.Printf("\nFlotte: %v/%v Nodes ready · GPU %.0f %% · Queue p95 %.0f ms · heute %s\n", s["nodes_ready"], s["nodes_total"], s["gpu_util"].(float64)*100, s["queue_wait_p95_ms"], eur(s["cost_today_micro_eur"]))
	case "dots":
		var dots []map[string]any
		newClient().do("GET", "/dots", nil, &dots)
		var rows [][]string
		for _, d := range dots {
			dd := d["dot"].(map[string]any)
			rows = append(rows, []string{fmt.Sprint(dd["id"]), fmt.Sprint(dd["name"]), fmt.Sprint(dd["kind"]), fmt.Sprint(dd["privacy_mode"])})
		}
		table("ID\tNAME\tART\tPRIVACY", rows)
	case "chat":
		if len(args) < 2 {
			die("nutzung: fyctl chat <dot-id> <text>")
		}
		c := newClient()
		var res map[string]any
		c.do("POST", "/dots/"+args[0]+"/chat", map[string]string{"text": strings.Join(args[1:], " ")}, &res)
		id := fmt.Sprint(res["run_id"])
		for i := 0; i < 600; i++ {
			var run map[string]any
			c.do("GET", "/runs/"+id, nil, &run)
			st := run["run"].(map[string]any)["status"]
			if st == "succeeded" || st == "failed" || st == "cancelled" || st == "waiting" {
				for _, e := range run["events"].([]any) {
					ev := e.(map[string]any)
					if ev["type"] == "message_out" {
						fmt.Println(ev["payload"].(map[string]any)["text"])
					}
				}
				if st != "succeeded" {
					fmt.Println("status:", st)
				}
				return
			}
			time.Sleep(time.Second)
		}
		die("zeitüberschreitung")
	case "runs":
		if len(args) < 1 {
			die("nutzung: fyctl runs <dot-id>")
		}
		var runs []map[string]any
		newClient().do("GET", "/dots/"+args[0]+"/runs?limit=30", nil, &runs)
		var rows [][]string
		for _, r := range runs {
			rows = append(rows, []string{fmt.Sprint(r["id"]), fmt.Sprint(r["kind"]), fmt.Sprint(r["status"]), fmt.Sprint(r["tainted"]), eur(r["cost_micro_eur"])})
		}
		table("RUN\tART\tSTATUS\tTAINTED\tKOSTEN", rows)
	case "run":
		if len(args) < 1 {
			die("nutzung: fyctl run <run-id>")
		}
		var run map[string]any
		newClient().do("GET", "/runs/"+args[0], nil, &run)
		for _, e := range run["events"].([]any) {
			ev := e.(map[string]any)
			b, _ := json.Marshal(ev["payload"])
			p := string(b)
			if len(p) > 220 {
				p = p[:220] + "…"
			}
			fmt.Printf("%3v %-19s %s\n", ev["seq"], ev["type"], p)
		}
	case "approvals":
		var list []map[string]any
		newClient().do("GET", "/approvals?status=pending", nil, &list)
		var rows [][]string
		for _, a := range list {
			prev, _ := a["preview"].(map[string]any)
			rows = append(rows, []string{fmt.Sprint(a["id"]), fmt.Sprint(a["tool"]), fmt.Sprint(a["class"]), fmt.Sprint(a["risk"]), fmt.Sprint(prev["summary"])})
		}
		table("ID\tTOOL\tKLASSE\tRISIKO\tVORSCHAU", rows)
	case "approve", "deny":
		if len(args) < 1 {
			die("id fehlt")
		}
		var res map[string]any
		newClient().do("POST", "/approvals/"+args[0]+"/resolve", map[string]any{"approve": os.Args[1] == "approve", "reason": strings.Join(args[1:], " ")}, &res)
		fmt.Println(res["status"])
	case "audit":
		fs := flag.NewFlagSet("audit", flag.ExitOnError)
		db := fs.String("db", os.Getenv("FYLGJA_DATABASE_URL"), "datenbank-url (offline-prüfung)")
		if len(args) == 0 || args[0] != "verify" {
			die("nutzung: fyctl audit verify [--db URL]")
		}
		_ = fs.Parse(args[1:])
		if *db != "" {
			pool, err := store.Open(context.Background(), *db)
			if err != nil {
				die(err.Error())
			}
			n, err := (&audit.PG{Pool: pool}).VerifyAll(context.Background())
			if err != nil {
				die(fmt.Sprintf("KETTE GEBROCHEN nach %d einträgen: %v", n, err))
			}
			fmt.Printf("ok: %d einträge, kette intakt\n", n)
			return
		}
		var res map[string]any
		newClient().do("GET", "/audit/verify", nil, &res)
		if res["ok"] != true {
			die(fmt.Sprintf("KETTE GEBROCHEN: %v", res["error"]))
		}
		fmt.Printf("ok: %v einträge, kette intakt\n", res["entries"])
	case "fleet":
		c := newClient()
		if len(args) >= 2 && args[0] == "add-node" {
			region := ""
			if len(args) > 2 {
				region = args[2]
			}
			var res map[string]any
			c.do("POST", "/fleet/nodes", map[string]string{"name": args[1], "region": region}, &res)
			fmt.Println("Node angelegt. Auf dem Server setzen und fylgja-node starten:")
			env := res["env"].(map[string]any)
			keys := make([]string, 0, len(env))
			for k := range env {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				fmt.Printf("export %s='%v'\n", k, env[k])
			}
			return
		}
		if len(args) >= 2 && (args[0] == "drain" || args[0] == "terminate") {
			c.do("POST", "/fleet/nodes/"+args[1]+"/"+args[0], nil, nil)
			fmt.Println("ok")
			return
		}
		var fo map[string]any
		c.do("GET", "/fleet/overview", nil, &fo)
		var rows [][]string
		for _, n := range fo["nodes"].([]any) {
			nn := n.(map[string]any)
			m := nn["metrics"].(map[string]any)
			rows = append(rows, []string{fmt.Sprint(nn["name"]), fmt.Sprint(nn["state"]), fmt.Sprint(nn["gpu_model"]),
				fmt.Sprintf("%.0f%%", m["gpu_util"].(float64)*100), fmt.Sprintf("%.0f%%", m["kv_cache_util"].(float64)*100), eur(nn["hourly_cost_micro_eur"]) + "/h"})
		}
		table("NODE\tSTATUS\tGPU\tAUSLASTUNG\tKV-CACHE\tKOSTEN", rows)
		fmt.Println()
		rows = nil
		for _, d := range fo["router"].(map[string]any)["deployments"].([]any) {
			dd := d.(map[string]any)
			rows = append(rows, []string{fmt.Sprint(dd["name"]), fmt.Sprint(dd["model"]), fmt.Sprint(dd["provider"]), fmt.Sprint(dd["state"]),
				fmt.Sprintf("%v/%v", dd["inflight"], dd["max_concurrency"]), fmt.Sprint(dd["breaker"])})
		}
		table("DEPLOYMENT\tMODELL\tPROVIDER\tSTATUS\tSLOTS\tBREAKER", rows)
	case "memory":
		if len(args) < 3 || args[0] != "export" {
			die("nutzung: fyctl memory export <dot-id> <ordner>")
		}
		var files map[string]string
		newClient().do("GET", "/dots/"+args[1]+"/memory/export", nil, &files)
		for p, content := range files {
			full := filepath.Join(args[2], filepath.Clean("/"+p))
			_ = os.MkdirAll(filepath.Dir(full), 0o755)
			if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
				die(err.Error())
			}
		}
		fmt.Printf("%d dateien exportiert nach %s\n", len(files), args[2])
	case "pause-all":
		hard := len(args) > 0 && args[0] == "--hard"
		var res map[string]any
		newClient().do("POST", "/emergency/pause-all", map[string]bool{"hard": hard}, &res)
		fmt.Printf("alle fylgjur pausiert (gestoppte läufe: %v)\n", res["stopped_runs"])
	case "resume-all":
		newClient().do("POST", "/emergency/resume-all", nil, nil)
		fmt.Println("fortgesetzt")
	default:
		fmt.Print(help)
		os.Exit(2)
	}
}
