# Captain budget rules

Captain loads budget rules from the database, from one YAML file per rule in
`~/.config/captain/budgets`, and from directories listed in
`runtime.budgetDirs` in `~/.captain.yaml`. The database is the default catalog
write target. A YAML filename supplies the catalog key; the file contains the
authored rule only:

```yaml
name: Team monthly budget
match:
  dimensions:
    team: platform-*
  models:
    - anthropic/claude-*
groupBy:
  - team
amount: 100
window: now/M
```

Amounts and ledger comparisons are USD. Windows are relative Elasticsearch
datemath such as `now/M`, `now/w`, `now-30d`, or `now-1M`; `M` is month and `m`
is minute. Absolute anchors and future starts are rejected.

Enforcement uses completed `captain_model_calls` spend only. It does not reserve
funds or account for in-flight spend, so concurrent calls and a single call that
costs more than the remaining amount can overshoot a limit before the next
admission is refused.
