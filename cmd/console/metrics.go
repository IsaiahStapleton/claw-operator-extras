/*
Copyright 2026 Red Hat.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Prometheus text exposition. Gauge semantics throughout: values are
// recomputed snapshots that can decrease if files rotate, so counters would
// be misleading.

package main

import (
	"fmt"
	"sort"
	"strings"
)

func escapeLabel(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, `"`, `\"`)
	return strings.ReplaceAll(v, "\n", `\n`)
}

var metricFamilies = []struct{ name, typ, help string }{
	{"agent_console_runs_total", "gauge", "Runs observed in current trajectory files, by agent and outcome"},
	{"agent_console_tokens_total", "gauge", "Token counts observed in current trajectory files, by agent, model, and kind"},
	{"agent_console_active_runs", "gauge", "Currently running (unfinished, non-stale) runs by agent"},
	{"agent_console_run_seconds_sum", "gauge", "Sum of finished run durations in seconds by agent"},
	{"agent_console_run_seconds_count", "gauge", "Count of finished runs contributing to run_seconds_sum by agent"},
	{"agent_console_unparseable_lines_total", "gauge", "Total unparseable JSONL lines across all scanned files"},
	{"agent_console_data_ok", "gauge", "Whether the last data scan succeeded (1=ok, 0=error)"},
}

func renderMetrics(snap *Snapshot) string {
	counters := map[string]float64{}
	inc := func(key string, v float64) { counters[key] += v }

	for _, r := range snap.Runs {
		inc(fmt.Sprintf(`agent_console_runs_total{agent="%s",outcome="%s"}`, escapeLabel(r.Agent), escapeLabel(r.Outcome)), 1)
		if r.Model != "" {
			inc(fmt.Sprintf(`agent_console_tokens_total{agent="%s",model="%s",kind="input"}`, escapeLabel(r.Agent), escapeLabel(r.Model)), float64(r.Tokens.Input))
			inc(fmt.Sprintf(`agent_console_tokens_total{agent="%s",model="%s",kind="output"}`, escapeLabel(r.Agent), escapeLabel(r.Model)), float64(r.Tokens.Output))
		}
		if r.Outcome == "running" {
			inc(fmt.Sprintf(`agent_console_active_runs{agent="%s"}`, escapeLabel(r.Agent)), 1)
		}
		if r.Outcome == "ok" || r.Outcome == "error" {
			secs := float64(tsMillis(r.LastEventAt)-tsMillis(r.StartedAt)) / 1000
			if secs < 0 {
				secs = 0
			}
			inc(fmt.Sprintf(`agent_console_run_seconds_sum{agent="%s"}`, escapeLabel(r.Agent)), secs)
			inc(fmt.Sprintf(`agent_console_run_seconds_count{agent="%s"}`, escapeLabel(r.Agent)), 1)
		}
	}
	for _, a := range snap.Agents {
		inc(fmt.Sprintf(`agent_console_active_runs{agent="%s"}`, escapeLabel(a)), 0) // emit zeros so panels don't gap
	}

	byFamily := map[string][]string{}
	for k, v := range counters {
		for _, fam := range metricFamilies {
			if k == fam.name || strings.HasPrefix(k, fam.name+"{") {
				byFamily[fam.name] = append(byFamily[fam.name], fmt.Sprintf("%s %s", k, formatMetricValue(v)))
				break
			}
		}
	}
	standalone := map[string]string{
		"agent_console_unparseable_lines_total": fmt.Sprintf("agent_console_unparseable_lines_total %d", snap.BadLines),
		"agent_console_data_ok":                 fmt.Sprintf("agent_console_data_ok %d", boolToInt(snap.OK)),
	}

	var b strings.Builder
	for _, fam := range metricFamilies {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s %s\n", fam.name, fam.help, fam.name, fam.typ)
		if lines := byFamily[fam.name]; len(lines) > 0 {
			sort.Strings(lines)
			for _, l := range lines {
				b.WriteString(l + "\n")
			}
		} else if l, ok := standalone[fam.name]; ok {
			b.WriteString(l + "\n")
		}
	}
	return b.String()
}

func formatMetricValue(v float64) string {
	if v == float64(int64(v)) {
		return fmt.Sprintf("%d", int64(v))
	}
	return fmt.Sprintf("%g", v)
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
