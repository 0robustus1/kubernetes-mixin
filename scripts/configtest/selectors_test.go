// Copyright kubernetes-mixin Authors
// SPDX-License-Identifier: Apache-2.0

package configtest

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-jsonnet"
)

// selectorsRoot returns the repository root, which serves the imports of the snippets.
func selectorsRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func evaluate(t *testing.T, snippet string) (string, error) {
	t.Helper()
	root := selectorsRoot(t)
	vm := jsonnet.MakeVM()
	vm.Importer(&jsonnet.FileImporter{JPaths: []string{root, filepath.Join(root, "vendor")}})
	return vm.EvaluateAnonymousSnippet("selectors.jsonnet", snippet)
}

// selectorsSnippet evaluates expression with `selectors` bound to utils.selectors of the given kind
// and with the two additionalSelectors lists set to the given Jsonnet source.
func selectorsSnippet(kind, recording, alerting, expression string) string {
	return fmt.Sprintf(`local utils = import 'lib/utils.libsonnet';
local config = (import 'config.libsonnet')._config {
  additionalSelectors+: { recordingRules: %s, alertingRules: %s },
};
local selectors = utils.selectors(config, '%s');
%s
`, recording, alerting, kind, expression)
}

const (
	recordingSelectors = "additionalSelectors.recordingRules"
	alertingSelectors  = "additionalSelectors.alertingRules"
)

func TestSelectorValidationFailures(t *testing.T) {
	valid := `{ label: 'tenant', matcher: '=', value: 'a' }`
	for _, kind := range []struct{ name, config string }{
		{"recordingRules", recordingSelectors},
		{"alertingRules", alertingSelectors},
	} {
		for _, tc := range []struct {
			name  string
			entry string
			want  string
		}{
			{"string", `'tenant'`, " must contain only objects with the fields label, matcher and value, got \"tenant\""},
			{"array", `['tenant', '=', 'a']`, " must contain only objects with the fields label, matcher and value"},
			{"missingValue", `{ label: 'tenant', matcher: '=' }`, " must contain only objects with the fields label, matcher and value"},
			{"extraField", `{ label: 'tenant', matcher: '=', value: 'a', extra: true }`, " must contain only objects with the fields label, matcher and value"},
			{"invalidLabel", `{ label: 'tenant-id', matcher: '=', value: 'a' }`, " contains an invalid Prometheus label name: \"tenant-id\""},
			{"emptyLabel", `{ label: '', matcher: '=', value: 'a' }`, " contains an invalid Prometheus label name: \"\""},
			{"digitLabel", `{ label: '9tenant', matcher: '=', value: 'a' }`, " contains an invalid Prometheus label name: \"9tenant\""},
			{"numberLabel", `{ label: 1, matcher: '=', value: 'a' }`, " contains an invalid Prometheus label name: 1"},
			{"nameLabel", `{ label: '__name__', matcher: '=', value: 'a' }`, " must not select on __name__"},
			{"tildeMatcher", `{ label: 'tenant', matcher: '~', value: 'a' }`, " contains an unsupported matcher for label tenant, expected one of =, !=, =~ or !~: \"~\""},
			{"doubleEqualsMatcher", `{ label: 'tenant', matcher: '==', value: 'a' }`, " contains an unsupported matcher for label tenant"},
			{"numberValue", `{ label: 'tenant', matcher: '=', value: 1 }`, " must contain string values, got number for label tenant"},
			{"laterEntry", valid + `, { label: 'env', matcher: '=', value: 2 }`, " must contain string values, got number for label env"},
		} {
			t.Run(kind.name+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				list := "[" + tc.entry + "]"
				recording, alerting := "[]", "[]"
				if kind.name == "recordingRules" {
					recording = list
				} else {
					alerting = list
				}
				_, err := evaluate(t, selectorsSnippet(kind.name, recording, alerting, "selectors.matchers"))
				want := kind.config + tc.want
				if err == nil {
					t.Fatalf("expected an error containing %q", want)
				}
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("expected an error containing %q, got:\n%s", want, err)
				}
			})
		}
	}
}

func TestSelectorsRequireRecordedFailures(t *testing.T) {
	want := "additionalSelectors.alertingRules selectors used by recording-backed alerts must also be configured in additionalSelectors.recordingRules for KubeletPlegDurationHigh: tenant=\"a\""
	_, err := evaluate(t, selectorsSnippet("alertingRules",
		`[]`,
		`[{ label: 'tenant', matcher: '=', value: 'a' }]`,
		"selectors.requireRecorded('KubeletPlegDurationHigh')"))
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("expected an error containing %q, got %v", want, err)
	}

	// A selector with another matcher or value is a different selector.
	_, err = evaluate(t, selectorsSnippet("alertingRules",
		`[{ label: 'tenant', matcher: '=~', value: 'a' }]`,
		`[{ label: 'tenant', matcher: '=', value: 'a' }]`,
		"selectors.requireRecorded('KubeletPlegDurationHigh')"))
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("expected an error containing %q, got %v", want, err)
	}
}

func TestSelectorsRender(t *testing.T) {
	for _, tc := range []struct {
		name       string
		recording  string
		alerting   string
		kind       string
		expression string
		want       string
	}{
		{
			name:       "matchers",
			recording:  `[{ label: 'tenant', matcher: '=~', value: 'team-.*' }, { label: 'env', matcher: '!=', value: 'dev' }]`,
			alerting:   `[]`,
			kind:       "recordingRules",
			expression: "selectors.matchers",
			want:       `"tenant=~\"team-.*\",env!=\"dev\""`,
		},
		{
			name:       "escapedValue",
			recording:  `[{ label: 'tenant', matcher: '=', value: 'a"b\\c' }]`,
			alerting:   `[]`,
			kind:       "recordingRules",
			expression: "selectors.matchers",
			want:       `"tenant=\"a\\\"b\\\\c\""`,
		},
		{
			name:       "exporterSelector",
			recording:  `[{ label: 'tenant', matcher: '=', value: 'a' }]`,
			alerting:   `[]`,
			kind:       "recordingRules",
			expression: "selectors.apply(config).kubeletSelector",
			want:       `"job=\"kubelet\",tenant=\"a\""`,
		},
		{
			name:       "emptyExporterSelector",
			recording:  `[{ label: 'tenant', matcher: '=', value: 'a' }]`,
			alerting:   `[]`,
			kind:       "recordingRules",
			expression: "selectors.apply(config { kubeletSelector: '' }).kubeletSelector",
			want:       `"tenant=\"a\""`,
		},
		{
			name:       "bareMetric",
			recording:  `[{ label: 'tenant', matcher: '=', value: 'a' }]`,
			alerting:   `[]`,
			kind:       "recordingRules",
			expression: "selectors.apply(config).additionalSelectorBraces + selectors.apply(config).prefixedAdditionalSelector",
			want:       `"{tenant=\"a\"}tenant=\"a\","`,
		},
		{
			name:       "unconfigured",
			recording:  `[]`,
			alerting:   `[]`,
			kind:       "recordingRules",
			expression: "[selectors.apply(config).kubeletSelector, selectors.apply(config).additionalSelectorBraces, selectors.apply(config).prefixedAdditionalSelector]",
			want:       `["job=\"kubelet\"", "", ""]`,
		},
		{
			name:       "ownKindOnly",
			recording:  `[{ label: 'tenant', matcher: '=', value: 'a' }]`,
			alerting:   `[]`,
			kind:       "alertingRules",
			expression: "selectors.apply(config).kubeletSelector",
			want:       `"job=\"kubelet\""`,
		},
		{
			name:       "recorded",
			recording:  `[{ label: 'tenant', matcher: '=', value: 'a' }, { label: 'env', matcher: '=', value: 'prod' }]`,
			alerting:   `[{ label: 'tenant', matcher: '=', value: 'a' }]`,
			kind:       "alertingRules",
			expression: "selectors.requireRecorded('KubeletPlegDurationHigh')",
			want:       `["tenant=\"a\""]`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := evaluate(t, selectorsSnippet(tc.kind, tc.recording, tc.alerting, tc.expression))
			if err != nil {
				t.Fatal(err)
			}
			normalized := strings.Join(strings.Fields(got), " ")
			want := strings.Join(strings.Fields(tc.want), " ")
			normalized = strings.ReplaceAll(strings.ReplaceAll(normalized, "[ ", "["), " ]", "]")
			if normalized != want {
				t.Fatalf("expected %s, got %s", want, normalized)
			}
		})
	}
}

func TestSelectorsRenderUndefinedLists(t *testing.T) {
	// Overriding additionalSelectors without `+` leaves the lists empty.
	got, err := evaluate(t, `local utils = import 'lib/utils.libsonnet';
local config = (import 'config.libsonnet')._config { additionalSelectors: {} };
utils.selectors(config, 'recordingRules').matchers
`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(got) != `""` {
		t.Fatalf("expected no matchers, got %s", got)
	}
}

// TestSelectorsRecordingBackedAlerts checks the alerts that read recording rules: their selectors
// have to be configured for the recording rules as well, in every mode.
func TestSelectorsRecordingBackedAlerts(t *testing.T) {
	const tenant = `{ label: 'tenant', matcher: '=', value: 'a' }`
	for _, tc := range []struct {
		name   string
		module string
		config string
		alert  string
	}{
		{"apiserverBurnRate", "alerts/kube_apiserver.libsonnet", "", "KubeAPIErrorBudgetBurn"},
		{"apiserverBurnRateWithoutWindows", "alerts/kube_apiserver.libsonnet", noAPIWindows, "KubeAPIErrorBudgetBurn"},
		{"kubeletPleg", "alerts/kubelet.libsonnet", "", "KubeletPlegDurationHigh"},
		{"overcommit", "alerts/resource_alerts.libsonnet", "", "KubeCPUOvercommit and KubeMemoryOvercommit"},
		{"overcommitMultiCluster", "alerts/resource_alerts.libsonnet", "showMultiCluster: true,", "KubeCPUOvercommit and KubeMemoryOvercommit"},
	} {
		render := func(recording string) (string, error) {
			return evaluate(t, fmt.Sprintf(`local mixin = (import 'config.libsonnet') + (import '%s');
(mixin {
  _config+:: {
    %s
    %s
    additionalSelectors+: { recordingRules: [%s], alertingRules: [%s] },
  },
}).prometheusAlerts
`, tc.module, alertSelectors, tc.config, recording, tenant))
		}
		t.Run(tc.name+"/missing", func(t *testing.T) {
			t.Parallel()
			want := "additionalSelectors.alertingRules selectors used by recording-backed alerts must also be configured in additionalSelectors.recordingRules for " + tc.alert + ": tenant=\"a\""
			if _, err := render(""); err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("expected an error containing %q, got %v", want, err)
			}
		})
		t.Run(tc.name+"/recorded", func(t *testing.T) {
			t.Parallel()
			if _, err := render(tenant); err != nil {
				t.Fatal(err)
			}
		})
	}
}
