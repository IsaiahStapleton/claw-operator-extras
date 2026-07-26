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

// Gateway health: "disabled" when no URL is configured (an explicit state —
// never pretend to know). Short timeout so the console never hangs on it.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// GatewayHealth reports the OpenClaw gateway's /health status.
type GatewayHealth struct {
	Status string `json:"status"` // up | down | disabled
	Detail string `json:"detail,omitempty"`
}

func gatewayHealth(ctx context.Context, url string) GatewayHealth {
	if url == "" {
		return GatewayHealth{Status: "disabled"}
	}
	ctx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(url, "/")+"/health", nil)
	if err != nil {
		return GatewayHealth{Status: "down", Detail: "unreachable"}
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		detail := "unreachable"
		if ctx.Err() == context.DeadlineExceeded {
			detail = "timeout"
		}
		return GatewayHealth{Status: "down", Detail: detail}
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return GatewayHealth{Status: "down", Detail: fmt.Sprintf("HTTP %d", res.StatusCode)}
	}
	var body struct {
		OK     bool   `json:"ok"`
		Status string `json:"status"`
	}
	_ = json.NewDecoder(res.Body).Decode(&body)
	if body.OK {
		return GatewayHealth{Status: "up", Detail: body.Status}
	}
	return GatewayHealth{Status: "down", Detail: body.Status}
}
