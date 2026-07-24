package features

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestMetricFamilyCanaryRulesStayScopedAndShadowOnly(t *testing.T) {
	payload, err := os.ReadFile("../../deploy/prometheus/atlas-feature-canary.rules.yml")
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
	if len(config.Groups) != 1 || config.Groups[0].Name != "atlas-feature-canary" || config.Groups[0].Interval != "1m" {
		t.Fatalf("unexpected canary group: %+v", config.Groups)
	}
	if len(config.Groups[0].Rules) != 2 {
		t.Fatalf("expected count and delta rules, got %d", len(config.Groups[0].Rules))
	}
	for _, rule := range config.Groups[0].Rules {
		if !strings.HasPrefix(rule.Record, "atlas_canary:") || rule.Labels["canary_scope"] != "10.114.4.101" {
			t.Fatalf("rule escaped canary scope: %+v", rule)
		}
	}
	if !strings.Contains(config.Groups[0].Rules[0].Expr, `host_ip="10.114.4.101"`) {
		t.Fatalf("raw metric-family scan must stay scoped to one host: %s", config.Groups[0].Rules[0].Expr)
	}
}
