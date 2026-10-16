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
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/common/model"
	"go.yaml.in/yaml/v3"
)

type rulesDocument struct {
	Groups []ruleGroup `yaml:"groups"`
}

type ruleGroup struct {
	Name        string            `yaml:"name"`
	Interval    string            `yaml:"interval,omitempty"`
	QueryOffset string            `yaml:"query_offset,omitempty"`
	Limit       int               `yaml:"limit,omitempty"`
	Labels      map[string]string `yaml:"labels,omitempty"`
	Rules       []rule            `yaml:"rules"`
}

type rule struct {
	Record        string            `yaml:"record,omitempty"`
	Alert         string            `yaml:"alert,omitempty"`
	Expr          string            `yaml:"expr"`
	For           string            `yaml:"for,omitempty"`
	KeepFiringFor string            `yaml:"keep_firing_for,omitempty"`
	Labels        map[string]string `yaml:"labels,omitempty"`
	Annotations   map[string]string `yaml:"annotations,omitempty"`
}

func (r rule) name() string {
	if r.Record != "" {
		return r.Record
	}
	return r.Alert
}

type selectedGroup struct {
	ruleGroup
	effectiveInterval time.Duration
	queryOffset       time.Duration
}

type jsonnetRenderer struct {
	executor commandExecutor
	binary   string
	jpaths   []string
}

func (r jsonnetRenderer) Render(ctx context.Context, config string) (rulesDocument, error) {
	absoluteConfig, err := filepath.Abs(config)
	if err != nil {
		return rulesDocument{}, fmt.Errorf("resolve config path: %w", err)
	}

	args := make([]string, 0, len(r.jpaths)*2+3)
	for _, path := range r.jpaths {
		args = append(args, "-J", path)
	}
	expression := fmt.Sprintf(
		"std.manifestYamlDoc((import %s).prometheusRules)",
		strconv.Quote(filepath.ToSlash(absoluteConfig)),
	)
	args = append(args, "-S", "-e", expression)

	result, err := r.executor.Run(ctx, r.binary, args...)
	if err != nil {
		return rulesDocument{}, commandError("jsonnet", result, err)
	}
	if result.stdoutTruncated {
		return rulesDocument{}, fmt.Errorf("jsonnet output exceeded %d bytes", maxCommandStdoutBytes)
	}

	var document rulesDocument
	decoder := yaml.NewDecoder(strings.NewReader(string(result.stdout)))
	decoder.KnownFields(true)
	if err := decoder.Decode(&document); err != nil {
		return rulesDocument{}, fmt.Errorf("parse rendered rules: %w", err)
	}
	if err := validateRules(document); err != nil {
		return rulesDocument{}, err
	}
	return document, nil
}

func validateRules(document rulesDocument) error {
	seen := make(map[string]struct{}, len(document.Groups))
	for groupIndex, group := range document.Groups {
		if group.Name == "" {
			return fmt.Errorf("rendered group %d has no name", groupIndex+1)
		}
		if _, exists := seen[group.Name]; exists {
			return fmt.Errorf("rendered group name %q is duplicated", group.Name)
		}
		seen[group.Name] = struct{}{}
		for ruleIndex, currentRule := range group.Rules {
			if currentRule.name() == "" {
				return fmt.Errorf("group %q rule %d has neither record nor alert name", group.Name, ruleIndex+1)
			}
			if currentRule.Record != "" && currentRule.Alert != "" {
				return fmt.Errorf("group %q rule %d has both record and alert names", group.Name, ruleIndex+1)
			}
			if strings.TrimSpace(currentRule.Expr) == "" {
				return fmt.Errorf("group %q rule %q has an empty expression", group.Name, currentRule.name())
			}
		}
	}
	return nil
}

func selectGroups(groups []ruleGroup, requested []string, fallbackInterval time.Duration) ([]selectedGroup, error) {
	type groupSelector struct {
		pattern    string
		expression *regexp.Regexp
		matched    bool
	}

	selectors := make([]groupSelector, 0, len(requested))
	seenSelectors := make(map[string]struct{}, len(requested))
	for _, pattern := range requested {
		if _, seen := seenSelectors[pattern]; seen {
			continue
		}
		seenSelectors[pattern] = struct{}{}
		expression := "^" + strings.ReplaceAll(regexp.QuoteMeta(pattern), `\*`, ".+") + "$"
		selectors = append(selectors, groupSelector{
			pattern:    pattern,
			expression: regexp.MustCompile(expression),
		})
	}

	selected := make([]selectedGroup, 0, len(groups))
	for _, group := range groups {
		if len(selectors) > 0 {
			wanted := false
			for index := range selectors {
				if selectors[index].expression.MatchString(group.Name) {
					selectors[index].matched = true
					wanted = true
				}
			}
			if !wanted {
				continue
			}
		}

		interval := fallbackInterval
		if group.Interval != "" {
			parsed, err := model.ParseDuration(group.Interval)
			if err != nil {
				return nil, fmt.Errorf("group %q has invalid interval %q: %w", group.Name, group.Interval, err)
			}
			interval = time.Duration(parsed)
		}
		if interval <= 0 {
			return nil, fmt.Errorf("group %q has a non-positive interval", group.Name)
		}

		var queryOffset time.Duration
		if group.QueryOffset != "" {
			parsed, err := model.ParseDuration(group.QueryOffset)
			if err != nil {
				return nil, fmt.Errorf("group %q has invalid query offset %q: %w", group.Name, group.QueryOffset, err)
			}
			queryOffset = time.Duration(parsed)
			if queryOffset < 0 {
				return nil, fmt.Errorf("group %q has a negative query offset", group.Name)
			}
		}
		selected = append(selected, selectedGroup{
			ruleGroup:         group,
			effectiveInterval: interval,
			queryOffset:       queryOffset,
		})
	}

	missing := make([]string, 0, len(selectors))
	for _, selector := range selectors {
		if !selector.matched {
			missing = append(missing, selector.pattern)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("requested groups were not rendered: %s", strings.Join(missing, ", "))
	}
	if len(selected) == 0 {
		return nil, errorsNoGroups
	}
	return selected, nil
}

var errorsNoGroups = fmt.Errorf("no rule groups selected")
