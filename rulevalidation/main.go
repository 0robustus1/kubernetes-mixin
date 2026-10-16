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
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/alecthomas/kong"
)

const (
	exitSuccess = 0
	exitFailure = 1
	exitUsage   = 2
)

type cli struct {
	Config             string        `help:"Customized mixin entrypoint to render." required:"" type:"existingfile"`
	JPath              []string      `help:"Jsonnet import path. May be repeated." default:"vendor" sep:"none"`
	Group              []string      `help:"Exact rule group name to execute. May be repeated." sep:"none"`
	Time               string        `help:"Evaluation time as RFC3339 or a Unix timestamp. Defaults to one shared current time."`
	GroupTimeout       time.Duration `help:"Per-query timeout and cumulative group timeout budget." default:"1m"`
	EvaluationInterval time.Duration `help:"Interval for groups that do not declare one." default:"1m"`
	JsonnetBinary      string        `help:"Path or name of the Jsonnet executable." default:"jsonnet"`

	Promtool promtoolCommand `cmd:"" help:"Query a Prometheus-compatible datasource with promtool."`
	GCX      gcxCommand      `cmd:"" name:"gcx" help:"Query a Grafana Prometheus datasource with gcx."`
}

type promtoolCommand struct {
	Server     string `help:"Prometheus server URL." required:""`
	HTTPConfig string `help:"Promtool HTTP client configuration file." type:"existingfile"`
	Binary     string `help:"Path or name of the promtool executable." default:"promtool"`
}

type gcxCommand struct {
	Datasource string `help:"Grafana datasource UID. Uses configured datasource resolution when omitted."`
	Context    string `help:"GCX context name."`
	GCXConfig  string `name:"gcx-config" help:"Path to the GCX configuration file." type:"existingfile"`
	Binary     string `help:"Path or name of the gcx executable." default:"gcx"`
}

func main() {
	os.Exit(runCLI(os.Args[1:], os.Stdout, os.Stderr, osCommandExecutor{}))
}

func runCLI(args []string, stdout, stderr io.Writer, executor commandExecutor) int {
	var cfg cli
	parser, err := kong.New(
		&cfg,
		kong.Name("rulevalidation"),
		kong.Description("Render mixin recording rules and evaluate each rule query sequentially."),
		kong.Writers(stdout, stderr),
	)
	if err != nil {
		fmt.Fprintf(stderr, "configure command line: %v\n", err)
		return exitUsage
	}

	parsed, err := parser.Parse(args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitUsage
	}

	if cfg.GroupTimeout <= 0 {
		fmt.Fprintln(stderr, "--group-timeout must be greater than zero")
		return exitUsage
	}
	if cfg.EvaluationInterval <= 0 {
		fmt.Fprintln(stderr, "--evaluation-interval must be greater than zero")
		return exitUsage
	}

	evaluationTime, err := resolveEvaluationTime(cfg.Time, time.Now())
	if err != nil {
		fmt.Fprintf(stderr, "invalid --time: %v\n", err)
		return exitUsage
	}

	var backend queryBackend
	switch parsed.Command() {
	case "promtool":
		backend = newPromtoolBackend(executor, cfg.Promtool)
	case "gcx":
		backend = newGCXBackend(executor, cfg.GCX)
	default:
		fmt.Fprintf(stderr, "unsupported command %q\n", parsed.Command())
		return exitUsage
	}

	renderer := jsonnetRenderer{
		executor: executor,
		binary:   cfg.JsonnetBinary,
		jpaths:   cfg.JPath,
	}
	document, err := renderer.Render(context.Background(), cfg.Config)
	if err != nil {
		fmt.Fprintf(stderr, "render rules: %v\n", err)
		return exitFailure
	}

	groups, err := selectGroups(document.Groups, cfg.Group, cfg.EvaluationInterval)
	if err != nil {
		fmt.Fprintf(stderr, "select groups: %v\n", err)
		return exitFailure
	}

	results, err := executeGroups(context.Background(), groups, backend, evaluationTime, cfg.GroupTimeout, time.Now)
	if err != nil {
		fmt.Fprintf(stderr, "execute groups: %v\n", err)
		return exitFailure
	}
	writeReport(stdout, results)
	if !allGroupsSucceeded(results) {
		return exitFailure
	}
	return exitSuccess
}

func resolveEvaluationTime(value string, now time.Time) (string, error) {
	if value == "" || value == "now" {
		return now.UTC().Format(time.RFC3339Nano), nil
	}
	if _, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return value, nil
	}
	if _, err := parseUnixTimestamp(value); err == nil {
		return value, nil
	}
	return "", errors.New("must be RFC3339, a Unix timestamp, or 'now'")
}
