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
	"os"
	"path/filepath"
	"testing"
)

func TestRunCLIUsesInjectedExecutors(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	config := filepath.Join(directory, "mixin.libsonnet")
	if err := os.WriteFile(config, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	executor := &fakeCommandExecutor{responses: []fakeCommandResponse{
		{result: commandResult{stdout: []byte(`groups:
- name: selected
  rules:
  - record: selected:metric
    expr: up
`)}},
		{result: commandResult{stdout: []byte(`[{"metric":{"job":"one"},"value":[1,"1"]}]`)}},
	}}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := runCLI([]string{
		"--config", config,
		"--group", "select*",
		"--time", "2026-10-16T12:00:00Z",
		"--jsonnet-binary", "fake-jsonnet",
		"promtool",
		"--server", "https://prometheus.example",
		"--binary", "fake-promtool",
	}, &stdout, &stderr, executor)
	if exitCode != exitSuccess {
		t.Fatalf("runCLI() exit = %d, stderr = %s", exitCode, stderr.String())
	}
	if len(executor.calls) != 2 {
		t.Fatalf("command calls = %#v", executor.calls)
	}
	if executor.calls[0].name != "fake-jsonnet" || executor.calls[1].name != "fake-promtool" {
		t.Fatalf("command calls = %#v", executor.calls)
	}
	if !bytes.Contains(stdout.Bytes(), []byte("GROUP PASS")) {
		t.Fatalf("stdout = %s", stdout.String())
	}
}
