// Copyright kubernetes-mixin Authors
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestJSONNetRendererCapturesRulesInMemory(t *testing.T) {
	t.Parallel()

	executor := &fakeCommandExecutor{responses: []fakeCommandResponse{{result: commandResult{stdout: []byte(`
groups:
- name: first
  interval: 3m
  labels:
    environment: test
  rules:
  - record: first:metric
    expr: up
    labels:
      source: mixin
`)}}}}
	renderer := jsonnetRenderer{executor: executor, binary: "custom-jsonnet", jpaths: []string{"vendor", "extra"}}
	document, err := renderer.Render(context.Background(), "configured-mixin.libsonnet")
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(document.Groups) != 1 || document.Groups[0].Rules[0].Record != "first:metric" {
		t.Fatalf("Render() document = %#v", document)
	}
	if len(executor.calls) != 1 {
		t.Fatalf("command calls = %d, want 1", len(executor.calls))
	}
	call := executor.calls[0]
	if call.name != "custom-jsonnet" {
		t.Fatalf("command name = %q", call.name)
	}
	wantPrefix := []string{"-J", "vendor", "-J", "extra", "-S", "-e"}
	if !reflect.DeepEqual(call.args[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("command args = %#v, want prefix %#v", call.args, wantPrefix)
	}
	absolute, err := filepath.Abs("configured-mixin.libsonnet")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(call.args[len(call.args)-1], filepath.ToSlash(absolute)) {
		t.Fatalf("expression %q does not import absolute config path", call.args[len(call.args)-1])
	}
	if !strings.Contains(call.args[len(call.args)-1], ").prometheusRules") {
		t.Fatalf("expression %q does not select prometheusRules", call.args[len(call.args)-1])
	}
}

func TestSelectGroupsPreservesRenderedOrderAndUsesIntervals(t *testing.T) {
	t.Parallel()

	groups := []ruleGroup{
		{Name: "first", Interval: "2m"},
		{Name: "second", QueryOffset: "30s"},
		{Name: "third", Interval: "1h30m"},
	}
	selected, err := selectGroups(groups, []string{"third", "second", "third"}, time.Minute)
	if err != nil {
		t.Fatalf("selectGroups() error = %v", err)
	}
	if got := []string{selected[0].Name, selected[1].Name}; !reflect.DeepEqual(got, []string{"second", "third"}) {
		t.Fatalf("selected order = %#v", got)
	}
	if selected[0].effectiveInterval != time.Minute || selected[1].effectiveInterval != 90*time.Minute {
		t.Fatalf("effective intervals = %s, %s", selected[0].effectiveInterval, selected[1].effectiveInterval)
	}
	if selected[0].queryOffset != 30*time.Second || selected[1].queryOffset != 0 {
		t.Fatalf("query offsets = %s, %s", selected[0].queryOffset, selected[1].queryOffset)
	}
}

func TestSelectGroupsSupportsAnchoredGlobSelectors(t *testing.T) {
	t.Parallel()

	groups := []ruleGroup{
		{Name: "api.rules."},
		{Name: "api.rules.node"},
		{Name: "prefix-api.rules.node"},
		{Name: "apiXrules.node"},
		{Name: "api.rules.node.slo"},
		{Name: "api+rules.[]"},
		{Name: "api+rules.[node]"},
	}
	selected, err := selectGroups(
		groups,
		[]string{"api.rules.*", "api.rules.node", "api+rules.[*]", "api.rules.*"},
		time.Minute,
	)
	if err != nil {
		t.Fatalf("selectGroups() error = %v", err)
	}

	got := make([]string, 0, len(selected))
	for _, group := range selected {
		got = append(got, group.Name)
	}
	want := []string{"api.rules.node", "api.rules.node.slo", "api+rules.[node]"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("selected groups = %#v, want %#v", got, want)
	}
}

func TestSelectGroupsRejectsUnmatchedSelector(t *testing.T) {
	t.Parallel()

	_, err := selectGroups([]ruleGroup{{Name: "present-one"}}, []string{"present*", "missing*"}, time.Minute)
	if err == nil || !strings.Contains(err.Error(), "missing*") {
		t.Fatalf("selectGroups() error = %v, want unmatched selector error", err)
	}
}

func TestValidateRulesRejectsMalformedRules(t *testing.T) {
	t.Parallel()

	err := validateRules(rulesDocument{Groups: []ruleGroup{{Name: "group", Rules: []rule{{Record: "name"}}}}})
	if err == nil || !strings.Contains(err.Error(), "empty expression") {
		t.Fatalf("validateRules() error = %v", err)
	}
}
