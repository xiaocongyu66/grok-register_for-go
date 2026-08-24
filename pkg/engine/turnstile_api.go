package engine

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// SolveTurnstileViaAPI calls an external HTTP solver API compatible with the
// theyka/d3vin/solver-gateway interface:
//   GET /turnstile?url=&sitekey=&action=&cdata=  → {"task_id":"...","id":"..."}
//   GET /result?id=                               → {"status":"success","value":"<token>"}
//
// Env:
//   TURNSTILE_API_URL              base URL (e.g. http://127.0.0.1:5080)
//   TURNSTILE_API_TOKEN            optional bearer/X-API-Key token
//   TURNSTILE_API_TIMEOUT          total solve budget seconds (default 120)
//   TURNSTILE_API_POLL_INTERVAL_MS poll interval (default 500)
//   TURNSTILE_API_ACTION           optional action param
//   TURNSTILE_API_CDATA            optional cdata param
func SolveTurnstileViaAPI(siteKey string) (string, error) {
	base := strings.TrimRight(strings.TrimSpace(envFirst("TURNSTILE_API_URL")), "/")
	if base == "" {
		return "", fmt.Errorf("TURNSTILE_API_URL not set")
	}
	timeoutSec := envInt("TURNSTILE_API_TIMEOUT", 120)
	pollMs := envInt("TURNSTILE_API_POLL_INTERVAL_MS", 500)
	if pollMs < 100 {
		pollMs = 100
	}
	token := envFirst("TURNSTILE_API_TOKEN", "TURNSTILE_TOKEN", "SOLVER_API_TOKEN")
	action := envFirst("TURNSTILE_API_ACTION")
	cdata := envFirst("TURNSTILE_API_CDATA")

	hc := &http.Client{Timeout: time.Duration(timeoutSec) * time.Second}

	// Submit job
	q := url.Values{}
	q.Set("url", SignupURLGrok)
	q.Set("sitekey", siteKey)
	if action != "" {
		q.Set("action", action)
	}
	if cdata != "" {
		q.Set("cdata", cdata)
	}
	submitURL := base + "/turnstile?" + q.Encode()

	jobID, err := apiGet(hc, submitURL, token, "task_id", "id")
	if err != nil {
		return "", fmt.Errorf("submit: %w", err)
	}
	if jobID == "" {
		return "", fmt.Errorf("submit: empty job id")
	}

	// Poll result
	deadline := time.Now().Add(time.Duration(timeoutSec) * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(time.Duration(pollMs) * time.Millisecond)
		resultURL := base + "/result?id=" + url.QueryEscape(jobID)
		status, value, err := apiResult(hc, resultURL, token)
		if err != nil {
			continue
		}
		switch status {
		case "success":
			if value != "" {
				return value, nil
			}
		case "fail", "error", "expired":
			return "", fmt.Errorf("solver: %s %s", status, value)
		case "process", "pending":
			// keep polling
		}
	}
	return "", fmt.Errorf("solver: timeout after %ds", timeoutSec)
}

// apiGet submits a solve job and extracts the job ID from the JSON response,
// trying multiple keys for compatibility (task_id / id).
func apiGet(hc *http.Client, urlStr, token string, idKeys ...string) (string, error) {
	req, err := http.NewRequest("GET", urlStr, nil)
	if err != nil {
		return "", err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-API-Key", token)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("http %d: %s", resp.StatusCode, truncStr(string(body), 120))
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		// Some solvers return id as plain text
		s := strings.TrimSpace(string(body))
		if s != "" && len(s) < 200 {
			return s, nil
		}
		return "", fmt.Errorf("json: %w", err)
	}
	for _, k := range idKeys {
		if v, ok := m[k].(string); ok && v != "" {
			return v, nil
		}
	}
	// d3vin/theyka sometimes nest under "data"
	if data, ok := m["data"].(map[string]any); ok {
		for _, k := range idKeys {
			if v, ok := data[k].(string); ok && v != "" {
				return v, nil
			}
		}
	}
	return "", nil
}

// apiResult polls /result and returns (status, value).
func apiResult(hc *http.Client, urlStr, token string) (string, string, error) {
	req, err := http.NewRequest("GET", urlStr, nil)
	if err != nil {
		return "", "", err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-API-Key", token)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode >= 400 {
		return "error", "", fmt.Errorf("http %d", resp.StatusCode)
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return "", "", err
	}
	status, _ := m["status"].(string)
	value, _ := m["value"].(string)
	if status == "" {
		status = "pending"
	}
	return status, value, nil
}

func truncStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
