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
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

type queryBackend interface {
	Query(ctx context.Context, expression, evaluationTime string) (int, error)
}

type promtoolBackend struct {
	executor   commandExecutor
	binary     string
	server     string
	httpConfig string
}

func newPromtoolBackend(executor commandExecutor, config promtoolCommand) promtoolBackend {
	return promtoolBackend{
		executor:   executor,
		binary:     config.Binary,
		server:     config.Server,
		httpConfig: config.HTTPConfig,
	}
}

func (b promtoolBackend) Query(ctx context.Context, expression, evaluationTime string) (int, error) {
	args := []string{"query", "-o", "json"}
	if b.httpConfig != "" {
		args = append(args, "--http.config.file", b.httpConfig)
	}
	args = append(args, "instant", "--time", evaluationTime, "--", b.server, expression)
	result, err := b.executor.Run(ctx, b.binary, args...)
	if err != nil {
		return 0, commandError("promtool query instant", result, err)
	}
	if result.stdoutTruncated {
		return 0, fmt.Errorf("promtool query instant output exceeded %d bytes", maxCommandStdoutBytes)
	}
	count, err := countPromtoolSeries(result.stdout)
	if err != nil {
		return 0, fmt.Errorf("parse promtool response: %w", err)
	}
	return count, nil
}

type gcxBackend struct {
	executor   commandExecutor
	binary     string
	datasource string
	context    string
	config     string
}

func newGCXBackend(executor commandExecutor, config gcxCommand) gcxBackend {
	return gcxBackend{
		executor:   executor,
		binary:     config.Binary,
		datasource: config.Datasource,
		context:    config.Context,
		config:     config.GCXConfig,
	}
}

func (b gcxBackend) Query(ctx context.Context, expression, evaluationTime string) (int, error) {
	args := make([]string, 0, 14)
	if b.config != "" {
		args = append(args, "--config", b.config)
	}
	if b.context != "" {
		args = append(args, "--context", b.context)
	}
	args = append(args, "metrics", "query", "--expr", expression, "--time", evaluationTime, "-o", "json")
	if b.datasource != "" {
		args = append(args, "--datasource", b.datasource)
	}
	result, err := b.executor.Run(ctx, b.binary, args...)
	if err != nil {
		return 0, commandError("gcx metrics query", result, err)
	}
	if result.stdoutTruncated {
		return 0, fmt.Errorf("gcx metrics query output exceeded %d bytes", maxCommandStdoutBytes)
	}
	count, err := countGCXSeries(result.stdout)
	if err != nil {
		return 0, fmt.Errorf("parse gcx response: %w", err)
	}
	return count, nil
}

func countPromtoolSeries(output []byte) (int, error) {
	var value any
	if err := json.Unmarshal(output, &value); err != nil {
		return 0, err
	}
	items, ok := value.([]any)
	if !ok {
		return 0, fmt.Errorf("expected a JSON array, got %T", value)
	}
	if len(items) == 0 {
		return 0, nil
	}
	if _, vector := items[0].(map[string]any); vector {
		return len(items), nil
	}
	if len(items) == 2 {
		return 0, nil // Scalar and string results are values, not time series.
	}
	return 0, fmt.Errorf("unexpected JSON array shape")
}

func countGCXSeries(output []byte) (int, error) {
	var response struct {
		Status string `json:"status"`
		Error  string `json:"error"`
		Data   struct {
			ResultType string          `json:"resultType"`
			Result     json.RawMessage `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(output, &response); err != nil {
		return 0, err
	}
	if response.Status != "success" {
		if response.Error != "" {
			return 0, fmt.Errorf("query status %q: %s", response.Status, boundedErrorText(response.Error))
		}
		return 0, fmt.Errorf("query status %q", response.Status)
	}

	switch response.Data.ResultType {
	case "vector", "matrix":
		var result []json.RawMessage
		if err := json.Unmarshal(response.Data.Result, &result); err != nil {
			return 0, err
		}
		return len(result), nil
	case "scalar", "string":
		return 0, nil
	default:
		return 0, fmt.Errorf("unexpected result type %q", response.Data.ResultType)
	}
}

func commandError(operation string, result commandResult, err error) error {
	detail := strings.TrimSpace(string(result.stderr))
	usedStdout := detail == ""
	if detail == "" {
		detail = strings.TrimSpace(string(result.stdout))
	}
	if len(detail) > 500 {
		detail = detail[:500] + "..."
	}
	if (!usedStdout && result.stderrTruncated) || (usedStdout && result.stdoutTruncated) {
		detail += " [output truncated]"
	}
	if detail == "" {
		return fmt.Errorf("%s: %w", operation, err)
	}
	return fmt.Errorf("%s: %w: %s", operation, err, detail)
}

func parseUnixTimestamp(value string) (float64, error) {
	timestamp, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, err
	}
	if math.IsNaN(timestamp) || math.IsInf(timestamp, 0) {
		return 0, fmt.Errorf("timestamp must be finite")
	}
	return timestamp, nil
}
