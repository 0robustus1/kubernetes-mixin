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
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
)

type ruleResult struct {
	Name           string
	Succeeded      bool
	Series         int
	SeriesKnown    bool
	Duration       time.Duration
	TimeoutPercent float64
	Error          string
}

type groupResult struct {
	Name            string
	Succeeded       bool
	Series          int
	SeriesComplete  bool
	Duration        time.Duration
	Timeout         time.Duration
	Interval        time.Duration
	TimeoutPercent  float64
	IntervalPercent float64
	Rules           []ruleResult
	FailureReasons  []string
}

func executeGroups(
	ctx context.Context,
	groups []selectedGroup,
	backend queryBackend,
	evaluationTime string,
	groupTimeout time.Duration,
	now func() time.Time,
) ([]groupResult, error) {
	groupEvaluationTimes := make([]string, len(groups))
	for index, group := range groups {
		adjusted, err := applyQueryOffset(evaluationTime, group.queryOffset)
		if err != nil {
			return nil, fmt.Errorf("apply query offset for group %q: %w", group.Name, err)
		}
		groupEvaluationTimes[index] = adjusted
	}

	results := make([]groupResult, 0, len(groups))
	for groupIndex, group := range groups {
		groupResult := groupResult{
			Name:           group.Name,
			Succeeded:      true,
			SeriesComplete: true,
			Timeout:        groupTimeout,
			Interval:       group.effectiveInterval,
			Rules:          make([]ruleResult, 0, len(group.Rules)),
		}

		for _, currentRule := range group.Rules {
			queryContext, cancel := context.WithTimeout(ctx, groupTimeout)
			started := now()
			series, err := backend.Query(queryContext, currentRule.Expr, groupEvaluationTimes[groupIndex])
			duration := now().Sub(started)
			if duration < 0 {
				duration = 0
			}
			contextError := queryContext.Err()
			cancel()
			if err == nil && contextError != nil {
				err = contextError
			}

			result := ruleResult{
				Name:           currentRule.name(),
				Succeeded:      err == nil,
				Series:         series,
				SeriesKnown:    err == nil,
				Duration:       duration,
				TimeoutPercent: percentage(duration, groupTimeout),
			}
			if err != nil {
				if contextError != nil {
					result.Error = boundedErrorText(fmt.Sprintf("query exceeded %s timeout: %v", groupTimeout, err))
				} else {
					result.Error = boundedErrorText(err.Error())
				}
				groupResult.Succeeded = false
				groupResult.SeriesComplete = false
			} else {
				groupResult.Series += series
			}
			groupResult.Duration += duration
			groupResult.Rules = append(groupResult.Rules, result)
		}

		groupResult.TimeoutPercent = percentage(groupResult.Duration, groupTimeout)
		groupResult.IntervalPercent = percentage(groupResult.Duration, group.effectiveInterval)
		if groupResult.Duration > groupTimeout {
			groupResult.Succeeded = false
			groupResult.FailureReasons = append(groupResult.FailureReasons, "cumulative duration exceeded group timeout")
		}
		if groupResult.Duration > group.effectiveInterval {
			groupResult.Succeeded = false
			groupResult.FailureReasons = append(groupResult.FailureReasons, "cumulative duration exceeded evaluation interval")
		}
		for _, result := range groupResult.Rules {
			if !result.Succeeded {
				groupResult.FailureReasons = append(groupResult.FailureReasons, "one or more rule queries failed")
				break
			}
		}
		results = append(results, groupResult)
	}
	return results, nil
}

func applyQueryOffset(evaluationTime string, offset time.Duration) (string, error) {
	if offset == 0 {
		return evaluationTime, nil
	}
	if parsed, err := time.Parse(time.RFC3339Nano, evaluationTime); err == nil {
		return parsed.Add(-offset).UTC().Format(time.RFC3339Nano), nil
	}
	timestamp, err := parseUnixTimestamp(evaluationTime)
	if err != nil {
		return "", fmt.Errorf("parse evaluation time %q", evaluationTime)
	}
	return strconv.FormatFloat(timestamp-offset.Seconds(), 'f', -1, 64), nil
}

func boundedErrorText(value string) string {
	const maximumErrorBytes = 1000
	if len(value) <= maximumErrorBytes {
		return value
	}
	return value[:maximumErrorBytes] + "... [truncated]"
}

func percentage(value, total time.Duration) float64 {
	if total <= 0 {
		return 0
	}
	return float64(value) / float64(total) * 100
}

func writeReport(output io.Writer, groups []groupResult) {
	writer := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
	for groupIndex, group := range groups {
		if groupIndex > 0 {
			fmt.Fprintln(writer)
		}
		fmt.Fprintf(writer, "GROUP\t%s\tinterval=%s\ttimeout=%s\n", group.Name, group.Interval, group.Timeout)
		fmt.Fprintln(writer, "STATUS\tRULE\tSERIES\tDURATION\tTIMEOUT USED")
		for _, rule := range group.Rules {
			status := "PASS"
			if !rule.Succeeded {
				status = "FAIL"
			}
			series := "-"
			if rule.SeriesKnown {
				series = fmt.Sprintf("%d", rule.Series)
			}
			fmt.Fprintf(
				writer,
				"%s\t%s\t%s\t%s\t%.2f%%\n",
				status,
				rule.Name,
				series,
				formatDuration(rule.Duration),
				rule.TimeoutPercent,
			)
			if rule.Error != "" {
				fmt.Fprintf(writer, "\tERROR: %s\n", strings.ReplaceAll(rule.Error, "\n", " "))
			}
		}

		status := "PASS"
		if !group.Succeeded {
			status = "FAIL"
		}
		series := fmt.Sprintf("%d", group.Series)
		if !group.SeriesComplete {
			series += " (partial)"
		}
		fmt.Fprintf(
			writer,
			"GROUP %s\t%s\tseries=%s\tduration=%s\ttimeout=%.2f%% interval=%.2f%%\n",
			status,
			group.Name,
			series,
			formatDuration(group.Duration),
			group.TimeoutPercent,
			group.IntervalPercent,
		)
		if len(group.FailureReasons) > 0 {
			fmt.Fprintf(writer, "\tREASON: %s\n", strings.Join(group.FailureReasons, "; "))
		}
	}
	_ = writer.Flush()
}

func formatDuration(duration time.Duration) string {
	return duration.Round(time.Millisecond).String()
}

func allGroupsSucceeded(groups []groupResult) bool {
	for _, group := range groups {
		if !group.Succeeded {
			return false
		}
	}
	return true
}
