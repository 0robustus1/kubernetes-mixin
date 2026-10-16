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
	"reflect"
	"strings"
	"testing"
)

func TestPromtoolBackendBuildsInstantQueryAndCountsVector(t *testing.T) {
	t.Parallel()

	executor := &fakeCommandExecutor{responses: []fakeCommandResponse{{result: commandResult{stdout: []byte(`[
{"metric":{"job":"one"},"value":[1,"1"]},
{"metric":{"job":"two"},"value":[1,"1"]}
]`)}}}}
	backend := newPromtoolBackend(executor, promtoolCommand{
		Server:     "https://prometheus.example",
		HTTPConfig: "http.yaml",
		Binary:     "custom-promtool",
	})
	count, err := backend.Query(context.Background(), `up{job="api"}`, "2026-10-16T12:00:00Z")
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	if count != 2 {
		t.Fatalf("Query() count = %d, want 2", count)
	}
	want := commandCall{
		name: "custom-promtool",
		args: []string{
			"query", "-o", "json", "--http.config.file", "http.yaml", "instant",
			"--time", "2026-10-16T12:00:00Z", "--", "https://prometheus.example", `up{job="api"}`,
		},
	}
	if !reflect.DeepEqual(executor.calls[0], want) {
		t.Fatalf("command call = %#v, want %#v", executor.calls[0], want)
	}
}

func TestQueryBackendsRejectTruncatedOutput(t *testing.T) {
	t.Parallel()

	executor := &fakeCommandExecutor{responses: []fakeCommandResponse{{result: commandResult{
		stdout:          []byte(`[]`),
		stdoutTruncated: true,
	}}}}
	backend := newPromtoolBackend(executor, promtoolCommand{Server: "https://prometheus.example", Binary: "promtool"})
	if _, err := backend.Query(context.Background(), "up", "2026-10-16T12:00:00Z"); err == nil {
		t.Fatal("Query() unexpectedly accepted truncated output")
	}
}

func TestGCXBackendBuildsInstantQueryAndCountsVector(t *testing.T) {
	t.Parallel()

	executor := &fakeCommandExecutor{responses: []fakeCommandResponse{{result: commandResult{stdout: []byte(`{
"status":"success",
"data":{"resultType":"vector","result":[{"metric":{"job":"one"},"value":[1,"1"]}]}
}`)}}}}
	backend := newGCXBackend(executor, gcxCommand{
		Datasource: "datasource-uid",
		Context:    "production",
		GCXConfig:  "gcx.yaml",
		Binary:     "custom-gcx",
	})
	count, err := backend.Query(context.Background(), "up", "1760616000")
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	if count != 1 {
		t.Fatalf("Query() count = %d, want 1", count)
	}
	want := commandCall{
		name: "custom-gcx",
		args: []string{
			"--config", "gcx.yaml", "--context", "production", "metrics", "query",
			"--expr", "up", "--time", "1760616000", "-o", "json", "--datasource", "datasource-uid",
		},
	}
	if !reflect.DeepEqual(executor.calls[0], want) {
		t.Fatalf("command call = %#v, want %#v", executor.calls[0], want)
	}
}

func TestResponseCountersHandleEmptyAndScalarResults(t *testing.T) {
	t.Parallel()

	if count, err := countPromtoolSeries([]byte(`[]`)); err != nil || count != 0 {
		t.Fatalf("countPromtoolSeries(empty) = %d, %v", count, err)
	}
	if count, err := countPromtoolSeries([]byte(`[1760616000,"1"]`)); err != nil || count != 0 {
		t.Fatalf("countPromtoolSeries(scalar) = %d, %v", count, err)
	}
	if count, err := countGCXSeries([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`)); err != nil || count != 0 {
		t.Fatalf("countGCXSeries(empty) = %d, %v", count, err)
	}
}

func TestGCXErrorResponseIsBounded(t *testing.T) {
	t.Parallel()

	response := `{"status":"error","error":"` + strings.Repeat("x", 5000) + `"}`
	_, err := countGCXSeries([]byte(response))
	if err == nil {
		t.Fatal("countGCXSeries() unexpectedly accepted error response")
	}
	if len(err.Error()) > 1100 || !strings.Contains(err.Error(), "[truncated]") {
		t.Fatalf("error length = %d, error = %q", len(err.Error()), err)
	}
}
