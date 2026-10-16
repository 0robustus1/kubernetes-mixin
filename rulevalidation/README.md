# Rule validation

`rulevalidation` renders the recording rules from a customized
kubernetes-mixin entrypoint and executes every selected rule expression as an
instant query. Groups and their rules are evaluated sequentially in their
rendered order.

The configuration file must follow the customization pattern documented in
the repository README. It imports kubernetes-mixin and returns the customized
mixin object, for example:

```jsonnet
local kubernetes = import 'kubernetes-mixin/mixin.libsonnet';

kubernetes {
  _config+:: {
    kubeStateMetricsSelector: 'job="kube-state-metrics"',
  },
}
```

The tool runs the equivalent of this command and keeps the rendered YAML in
memory:

```shell
jsonnet -J vendor -S -e 'std.manifestYamlDoc((import "mixin.libsonnet").prometheusRules)'
```

## Build

From the repository root:

```shell
make rulevalidation
```

The binary is written to `tmp/bin/rulevalidation`. The `jsonnet` binary and
the selected query binary must be available on `PATH`, unless their paths are
set with the corresponding command-line flags.

## Promtool

```shell
tmp/bin/rulevalidation \
  --config ./mixin.libsonnet \
  --group kube-apiserver.rules \
  --time 2026-10-16T12:00:00Z \
  --group-timeout 1m \
  promtool \
  --server http://localhost:9090
```

Use `--http-config` to pass a Prometheus HTTP client configuration file to
promtool.

## GCX

```shell
tmp/bin/rulevalidation \
  --config ./mixin.libsonnet \
  gcx \
  --datasource prometheus-datasource-uid \
  --context production
```

GCX can also resolve the datasource from its selected context. Use
`--gcx-config` to select a non-default GCX configuration file.

## Evaluation behavior

- `--group` performs an exact match and can be repeated. Without it, all
  rendered groups run.
- `--time` accepts RFC3339 or Unix timestamps. If omitted or set to `now`, the
  tool captures one current timestamp and passes it to every query.
- `--group-timeout` defaults to `1m`. It limits each query process and is also
  the cumulative duration budget for each group.
- `--evaluation-interval` defaults to `1m` and applies to groups without an
  explicit `interval`.
- A group-level `query_offset` is subtracted from the shared evaluation time,
  matching Prometheus rule evaluation behavior.
- A query returning no series is successful and reports zero series.
- Query failures do not stop the run. Every selected rule is invoked once,
  and the command exits nonzero after reporting all results.
- Command output is capped to prevent unexpectedly large query responses from
  exhausting memory. Exceeding the cap is reported as a query failure.
- A group succeeds only when all of its queries succeed and its cumulative
  duration fits both its timeout and effective interval.

Durations are measured around the query subprocess, so they include process
startup and network time. Group series counts are the sum of successful rule
query counts; they are marked partial when any rule query fails.

Each rule row includes its rule-level static labels in deterministic label-name
order. Group labels and labels returned dynamically by the query are not
included. `TIMEOUT USED` reports the individual query's percentage of the
group timeout, while `CUMULATIVE TIMEOUT USED` reports the percentage consumed
by that rule and all preceding rules in the group.

Query error details are prefixed with explicit rule and label fields, for
example `ERROR: rule=example:metric labels={variant="one"}: <message>`. Rules
without static labels use `labels=-`.

Instant-query commands do not write recording-rule results back to the
datasource. Later rules therefore see only series that already exist in the
datasource, not the temporary output of earlier validation queries.
