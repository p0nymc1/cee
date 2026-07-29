package sandbox

import (
	"errors"
	"testing"

	"github.com/cee-project/cee/entities"
)

func TestHealthyProbe(t *testing.T) {
	sb := NewSandbox()
	sb.RegisterProbe("api_check", func(ctx map[string]any) (bool, string, error) {
		return true, "", nil
	})
	result, err := sb.Probe(entities.ProbeRequest{ProbeRef: "api_check", DomainID: "finance", StepContext: map[string]any{}})
	if err != nil || !result.Healthy {
		t.Fatalf("expected healthy probe, got %+v err=%v", result, err)
	}
}

func TestProbeReportsFailureMode(t *testing.T) {
	sb := NewSandbox()
	sb.RegisterProbe("containment_check", func(ctx map[string]any) (bool, string, error) {
		return false, "would isolate a domain controller", nil
	})
	result, _ := sb.Probe(entities.ProbeRequest{ProbeRef: "containment_check", DomainID: "security", StepContext: map[string]any{"host": "dc01"}})
	if result.Healthy {
		t.Fatalf("expected unhealthy result")
	}
	if result.DetectedFailureMode != "would isolate a domain controller" {
		t.Fatalf("unexpected failure mode: %s", result.DetectedFailureMode)
	}
}

func TestMissingProbeReportsUnhealthy(t *testing.T) {
	sb := NewSandbox()
	result, _ := sb.Probe(entities.ProbeRequest{ProbeRef: "unregistered", DomainID: "finance", StepContext: map[string]any{}})
	if result.Healthy {
		t.Fatalf("expected unhealthy result for unregistered probe")
	}
}

func TestProbeErrorReportsUnhealthy(t *testing.T) {
	sb := NewSandbox()
	sb.RegisterProbe("erp_check", func(ctx map[string]any) (bool, string, error) {
		return false, "", errors.New("erp unreachable")
	})
	result, _ := sb.Probe(entities.ProbeRequest{ProbeRef: "erp_check", DomainID: "finance", StepContext: map[string]any{}})
	if result.Healthy || result.DetectedFailureMode != "erp unreachable" {
		t.Fatalf("unexpected result: %+v", result)
	}
}
