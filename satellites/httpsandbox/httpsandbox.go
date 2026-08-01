package httpsandbox

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/p0nymc1/cee/entities"
	"github.com/p0nymc1/cee/execution"
)

type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

// MaxResponseBytes caps how much of the sandbox service's response is read into
// memory. A remote sandbox is more plausibly third-party than the model
// endpoints, so an unbounded body from it is a real exhaustion risk.
const MaxResponseBytes = 8 << 20 // 8 MiB

type Config struct {
	BaseURL    string
	APIKey     string
	Image      string
	HTTPClient Doer
}

type Sandbox struct {
	cfg      Config
	client   Doer
	endpoint string
}

var _ execution.Prober = (*Sandbox)(nil)

func New(cfg Config) *Sandbox {
	client := cfg.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	return &Sandbox{
		cfg:      cfg,
		client:   client,
		endpoint: strings.TrimRight(cfg.BaseURL, "/") + "/rehearse",
	}
}

type rehearseRequest struct {
	Image   string   `json:"image,omitempty"`
	Command []string `json:"command"`
}

type rehearseResponse struct {
	ExitCode int    `json:"exit_code"`
	Output   string `json:"output"`
}

func (s *Sandbox) Probe(req entities.ProbeRequest) (entities.ProbeResult, error) {
	command, ok := probeCommand(req.StepContext)
	if !ok {
		return entities.ProbeResult{
			Healthy:             false,
			DetectedFailureMode: "httpsandbox: step context has no probe_command []string",
		}, nil
	}

	body, err := json.Marshal(rehearseRequest{Image: s.cfg.Image, Command: command})
	if err != nil {
		return entities.ProbeResult{}, err
	}

	httpReq, err := http.NewRequest(http.MethodPost, s.endpoint, bytes.NewReader(body))
	if err != nil {
		return entities.ProbeResult{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if s.cfg.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+s.cfg.APIKey)
	}

	resp, err := s.client.Do(httpReq)
	if err != nil {
		return entities.ProbeResult{
			Healthy:             false,
			DetectedFailureMode: "httpsandbox: sandbox service unavailable: " + err.Error(),
		}, nil
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseBytes+1))
	if err != nil {
		return entities.ProbeResult{
			Healthy:             false,
			DetectedFailureMode: "httpsandbox: reading response: " + err.Error(),
		}, nil
	}
	if int64(len(respBody)) > MaxResponseBytes {
		return entities.ProbeResult{
			Healthy:             false,
			DetectedFailureMode: fmt.Sprintf("httpsandbox: response exceeds the %d-byte limit", MaxResponseBytes),
		}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return entities.ProbeResult{
			Healthy:             false,
			DetectedFailureMode: fmt.Sprintf("httpsandbox: service returned %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody))),
		}, nil
	}

	var parsed rehearseResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return entities.ProbeResult{
			Healthy:             false,
			DetectedFailureMode: "httpsandbox: cannot decode response: " + err.Error(),
		}, nil
	}
	if parsed.ExitCode != 0 {
		return entities.ProbeResult{
			Healthy:             false,
			DetectedFailureMode: fmt.Sprintf("httpsandbox: probe exited %d: %s", parsed.ExitCode, strings.TrimSpace(parsed.Output)),
		}, nil
	}
	return entities.ProbeResult{Healthy: true}, nil
}

func probeCommand(ctx map[string]any) ([]string, bool) {
	switch v := ctx["probe_command"].(type) {
	case []string:
		return v, len(v) > 0
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			s, ok := item.(string)
			if !ok {
				return nil, false
			}
			out = append(out, s)
		}
		return out, len(out) > 0
	default:
		return nil, false
	}
}
