// Copyright kubernetes-mixin Authors
// SPDX-License-Identifier: Apache-2.0

package configtest

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/promql/parser"
)

type renderedRule struct {
	Record string `json:"record"`
	Alert  string `json:"alert"`
	Expr   string `json:"expr"`
}

type renderedGroups struct {
	Groups []struct {
		Name  string         `json:"name"`
		Rules []renderedRule `json:"rules"`
	} `json:"groups"`
}

type renderedMixin struct {
	Rules  renderedGroups `json:"rules"`
	Alerts renderedGroups `json:"alerts"`
}

// selectorCoverageConfig is extra `_config` source for a case of TestSelectorCoverage.
type selectorCoverageCase struct {
	name   string
	config string
}

func renderMixin(t *testing.T, config string) renderedMixin {
	t.Helper()
	out, err := evaluate(t, fmt.Sprintf(`local m = (import 'mixin.libsonnet') + (import 'rules/windows.libsonnet') + {
  _config+:: {
    %s
    additionalSelectors+: {
      // The alerting selector has to be configured for the recording rules as well.
      recordingRules: [
        { label: 'tenant', matcher: '=', value: 'a' },
        { label: 'recorded', matcher: '!~', value: 'x.+' },
      ],
      alertingRules: [{ label: 'tenant', matcher: '=', value: 'a' }],
    },
  },
};
{ rules: m.prometheusRules, alerts: m.prometheusAlerts }
`, config))
	if err != nil {
		t.Fatal(err)
	}
	var rendered renderedMixin
	if err := json.Unmarshal([]byte(out), &rendered); err != nil {
		t.Fatal(err)
	}
	return rendered
}

// vectorSelectors returns the vector selectors of an expression.
func vectorSelectors(t *testing.T, expr string) []*parser.VectorSelector {
	t.Helper()
	parsed, err := parser.NewParser(parser.Options{}).ParseExpr(expr)
	if err != nil {
		t.Fatalf("cannot parse %q: %v", expr, err)
	}
	var selectors []*parser.VectorSelector
	parser.Inspect(parsed, func(node parser.Node, _ []parser.Node) error {
		if vs, ok := node.(*parser.VectorSelector); ok {
			selectors = append(selectors, vs)
		}
		return nil
	})
	return selectors
}

func hasMatcher(vs *parser.VectorSelector, label string, matchType labels.MatchType, value string) bool {
	for _, m := range vs.LabelMatchers {
		if m.Name == label && m.Type == matchType && m.Value == value {
			return true
		}
	}
	return false
}

func hasLabelMatcher(vs *parser.VectorSelector, label string) bool {
	for _, m := range vs.LabelMatchers {
		if m.Name == label {
			return true
		}
	}
	return false
}

// metricName returns the metric a vector selector reads.
func metricName(vs *parser.VectorSelector) string {
	if vs.Name != "" {
		return vs.Name
	}
	for _, m := range vs.LabelMatchers {
		if m.Name == labels.MetricName {
			return m.Value
		}
	}
	return ""
}

// TestSelectorCoverage renders the whole mixin with additional selectors and checks every vector
// selector of every rule with the PromQL parser: selectors of raw metrics carry the configured
// matchers of their kind of rule, while references to the mixin's recording rules carry none.
func TestSelectorCoverage(t *testing.T) {
	for _, tc := range []selectorCoverageCase{
		{name: "default"},
		{name: "multiCluster", config: "showMultiCluster: true,"},
		{name: "joinLabels", config: "common_join_labels: ['label_team'],"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rendered := renderMixin(t, tc.config)

			recorded := map[string]bool{}
			for _, group := range rendered.Rules.Groups {
				for _, rule := range group.Rules {
					recorded[rule.Record] = true
				}
			}

			var failures []string
			check := func(kind string, groups renderedGroups, matchers func(*parser.VectorSelector) []string) {
				checked := 0
				for _, group := range groups.Groups {
					for _, rule := range group.Rules {
						name := rule.Record + rule.Alert
						for _, vs := range vectorSelectors(t, rule.Expr) {
							checked++
							metric := metricName(vs)
							if recorded[metric] {
								if hasLabelMatcher(vs, "tenant") || hasLabelMatcher(vs, "recorded") {
									failures = append(failures, fmt.Sprintf("%s %s/%s: recording rule reference %s has a configured matcher: %s", kind, group.Name, name, metric, vs))
								}
								continue
							}
							for _, missing := range matchers(vs) {
								failures = append(failures, fmt.Sprintf("%s %s/%s: %s is missing %s: %s", kind, group.Name, name, metric, missing, vs))
							}
						}
					}
				}
				if checked < 100 {
					t.Errorf("%s: only %d vector selectors were checked", kind, checked)
				}
			}

			check("recording", renderedGroups(rendered.Rules), func(vs *parser.VectorSelector) []string {
				var missing []string
				if !hasMatcher(vs, "tenant", labels.MatchEqual, "a") {
					missing = append(missing, `tenant="a"`)
				}
				if !hasMatcher(vs, "recorded", labels.MatchNotRegexp, "x.+") {
					missing = append(missing, `recorded!~"x.+"`)
				}
				return missing
			})
			check("alerting", renderedGroups(rendered.Alerts), func(vs *parser.VectorSelector) []string {
				var missing []string
				if !hasMatcher(vs, "tenant", labels.MatchEqual, "a") {
					missing = append(missing, `tenant="a"`)
				}
				if hasLabelMatcher(vs, "recorded") {
					missing = append(missing, "no recording-only matcher")
				}
				return missing
			})

			sort.Strings(failures)
			if len(failures) > 0 {
				t.Fatalf("%d selectors are wrong:\n%s", len(failures), strings.Join(failures, "\n"))
			}
		})
	}
}
