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
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

type queryResponse struct {
	series int
	err    error
}

type fakeQueryBackend struct {
	expressions []string
	times       []string
	responses   []queryResponse
}

type timeoutThenSuccessBackend struct {
	calls int
}

func (b *timeoutThenSuccessBackend) Query(ctx context.Context, _, _ string) (int, error) {
	b.calls++
	if b.calls == 1 {
		<-ctx.Done()
		return 0, ctx.Err()
	}
	return 1, nil
}

func (f *fakeQueryBackend) Query(_ context.Context, expression, evaluationTime string) (int, error) {
	f.expressions = append(f.expressions, expression)
	f.times = append(f.times, evaluationTime)
	response := f.responses[0]
	f.responses = f.responses[1:]
	return response.series, response.err
}

func TestExecuteGroupsRunsEveryRuleOnceAndContinuesAfterFailure(t *testing.T) {
	t.Parallel()

	backend := &fakeQueryBackend{responses: []queryResponse{
		{series: 2},
		{err: errors.New("datasource rejected query")},
		{series: 3},
	}}
	groups := []selectedGroup{{
		ruleGroup: ruleGroup{Name: "group", Rules: []rule{
			{Record: "one", Expr: "expr-one"},
			{Record: "two", Expr: "expr-two"},
			{Record: "three", Expr: "expr-three"},
		}},
		effectiveInterval: 20 * time.Second,
	}}
	times := []time.Time{
		time.Unix(0, 0), time.Unix(2, 0),
		time.Unix(2, 0), time.Unix(5, 0),
		time.Unix(5, 0), time.Unix(9, 0),
	}
	now := func() time.Time {
		value := times[0]
		times = times[1:]
		return value
	}

	results, err := executeGroups(context.Background(), groups, backend, "2026-10-16T12:00:00Z", 10*time.Second, now)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(backend.expressions, []string{"expr-one", "expr-two", "expr-three"}) {
		t.Fatalf("query expressions = %#v", backend.expressions)
	}
	if !reflect.DeepEqual(backend.times, []string{"2026-10-16T12:00:00Z", "2026-10-16T12:00:00Z", "2026-10-16T12:00:00Z"}) {
		t.Fatalf("query times = %#v", backend.times)
	}
	group := results[0]
	if group.Succeeded {
		t.Fatal("group succeeded despite a failed query")
	}
	if group.Series != 5 || group.SeriesComplete {
		t.Fatalf("group series = %d, complete = %t", group.Series, group.SeriesComplete)
	}
	if group.Duration != 9*time.Second || group.TimeoutPercent != 90 || group.IntervalPercent != 45 {
		t.Fatalf("group timing = %s, %.2f%%, %.2f%%", group.Duration, group.TimeoutPercent, group.IntervalPercent)
	}
	if len(group.Rules) != 3 || group.Rules[1].SeriesKnown || group.Rules[2].TimeoutPercent != 40 {
		t.Fatalf("rule results = %#v", group.Rules)
	}
}

func TestExecuteGroupsFailsBudgets(t *testing.T) {
	t.Parallel()

	backend := &fakeQueryBackend{responses: []queryResponse{{series: 1}, {series: 1}}}
	groups := []selectedGroup{{
		ruleGroup:         ruleGroup{Name: "slow", Rules: []rule{{Record: "one", Expr: "1"}, {Record: "two", Expr: "2"}}},
		effectiveInterval: 5 * time.Second,
	}}
	times := []time.Time{time.Unix(0, 0), time.Unix(4, 0), time.Unix(4, 0), time.Unix(8, 0)}
	now := func() time.Time {
		value := times[0]
		times = times[1:]
		return value
	}

	results, err := executeGroups(context.Background(), groups, backend, "2026-10-16T12:00:00Z", 7*time.Second, now)
	if err != nil {
		t.Fatal(err)
	}
	result := results[0]
	if result.Succeeded || len(result.FailureReasons) != 2 {
		t.Fatalf("result = %#v", result)
	}
}

func TestExecuteGroupsContinuesAfterQueryTimeout(t *testing.T) {
	t.Parallel()

	backend := &timeoutThenSuccessBackend{}
	groups := []selectedGroup{{
		ruleGroup: ruleGroup{Name: "group", Rules: []rule{
			{Record: "slow", Expr: "slow"},
			{Record: "fast", Expr: "fast"},
		}},
		effectiveInterval: time.Second,
	}}
	results, err := executeGroups(context.Background(), groups, backend, "2026-10-16T12:00:00Z", 5*time.Millisecond, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if backend.calls != 2 {
		t.Fatalf("query calls = %d, want 2", backend.calls)
	}
	if results[0].Succeeded || results[0].Rules[0].Succeeded || !results[0].Rules[1].Succeeded {
		t.Fatalf("results = %#v", results)
	}
	if !strings.Contains(results[0].Rules[0].Error, "query exceeded") {
		t.Fatalf("timeout error = %q", results[0].Rules[0].Error)
	}
}

func TestExecuteGroupsAppliesQueryOffset(t *testing.T) {
	t.Parallel()

	backend := &fakeQueryBackend{responses: []queryResponse{{series: 1}, {series: 1}}}
	groups := []selectedGroup{
		{
			ruleGroup:         ruleGroup{Name: "rfc3339", Rules: []rule{{Record: "one", Expr: "1"}}},
			effectiveInterval: time.Minute,
			queryOffset:       5 * time.Minute,
		},
		{
			ruleGroup:         ruleGroup{Name: "same-time", Rules: []rule{{Record: "two", Expr: "2"}}},
			effectiveInterval: time.Minute,
		},
	}
	if _, err := executeGroups(context.Background(), groups, backend, "2026-10-16T12:00:00Z", time.Minute, time.Now); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(backend.times, []string{"2026-10-16T11:55:00Z", "2026-10-16T12:00:00Z"}) {
		t.Fatalf("query times = %#v", backend.times)
	}

	if got, err := applyQueryOffset("1760616000.5", 500*time.Millisecond); err != nil || got != "1760616000" {
		t.Fatalf("applyQueryOffset(unix) = %q, %v", got, err)
	}
}

func TestExecuteGroupsBoundsStoredErrors(t *testing.T) {
	t.Parallel()

	backend := &fakeQueryBackend{responses: []queryResponse{{err: errors.New(strings.Repeat("x", 5000))}}}
	groups := []selectedGroup{{
		ruleGroup:         ruleGroup{Name: "group", Rules: []rule{{Record: "rule", Expr: "up"}}},
		effectiveInterval: time.Minute,
	}}
	results, err := executeGroups(context.Background(), groups, backend, "2026-10-16T12:00:00Z", time.Minute, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if got := results[0].Rules[0].Error; len(got) > 1020 || !strings.HasSuffix(got, "[truncated]") {
		t.Fatalf("stored error length = %d, suffix = %q", len(got), got[len(got)-20:])
	}
}

func TestWriteReportIncludesRuleAndGroupMetrics(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	writeReport(&output, []groupResult{{
		Name: "group", Succeeded: false, Series: 2, SeriesComplete: false,
		Duration: 2 * time.Second, Timeout: time.Minute, Interval: time.Minute,
		TimeoutPercent: 3.33, IntervalPercent: 3.33,
		Rules: []ruleResult{{
			Name: "rule", Succeeded: false, Duration: 2 * time.Second,
			TimeoutPercent: 3.33, Error: "query failed\nwith details",
		}},
		FailureReasons: []string{"one or more rule queries failed"},
	}})
	text := output.String()
	for _, wanted := range []string{"GROUP", "group", "FAIL", "rule", "2s", "3.33%", "2 (partial)", "query failed with details"} {
		if !strings.Contains(text, wanted) {
			t.Fatalf("report does not contain %q:\n%s", wanted, text)
		}
	}
}

func TestResolveEvaluationTimeUsesOneCurrentTimestamp(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 10, 16, 12, 0, 0, 123, time.FixedZone("offset", 3600))
	got, err := resolveEvaluationTime("", now)
	if err != nil {
		t.Fatal(err)
	}
	if got != "2026-10-16T11:00:00.000000123Z" {
		t.Fatalf("resolveEvaluationTime() = %q", got)
	}
	if _, err := resolveEvaluationTime("now-5m", now); err == nil {
		t.Fatal("resolveEvaluationTime(now-5m) unexpectedly succeeded")
	}
}
