package features

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestMetricFamilyRecordingRulesCoverFleetWithoutAffectingHealth(t *testing.T) {
	payload, err := os.ReadFile("../../deploy/prometheus/atlas-feature.rules.yml")
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Groups []struct {
			Name     string `yaml:"name"`
			Interval string `yaml:"interval"`
			Rules    []struct {
				Record string            `yaml:"record"`
				Expr   string            `yaml:"expr"`
				Labels map[string]string `yaml:"labels"`
			} `yaml:"rules"`
		} `yaml:"groups"`
	}
	if err := yaml.Unmarshal(payload, &config); err != nil {
		t.Fatal(err)
	}
	if len(config.Groups) != 1 || config.Groups[0].Name != "atlas-feature-recording" || config.Groups[0].Interval != "1m" {
		t.Fatalf("unexpected recording-rule group: %+v", config.Groups)
	}
	if len(config.Groups[0].Rules) != 2 {
		t.Fatalf("expected count and delta rules, got %d", len(config.Groups[0].Rules))
	}
	for _, rule := range config.Groups[0].Rules {
		if !strings.HasPrefix(rule.Record, "atlas:") || rule.Labels["owner"] != "atlas-health" {
			t.Fatalf("unexpected production rule contract: %+v", rule)
		}
		if strings.Contains(rule.Expr, "host_ip=") || rule.Labels["canary_scope"] != "" {
			t.Fatalf("fleet rule must not retain canary scope: %+v", rule)
		}
	}
	if !strings.Contains(config.Groups[0].Rules[0].Expr, `UUID!=""`) || !strings.Contains(config.Groups[0].Rules[0].Expr, `__name__=~"DCGM_FI_DEV_.+"`) {
		t.Fatalf("raw metric-family scan must stay constrained to identified DCGM GPU metrics: %s", config.Groups[0].Rules[0].Expr)
	}
}
