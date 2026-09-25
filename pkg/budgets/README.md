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

Amounts and settled-spend reports are USD. Windows are relative Elasticsearch
datemath such as `now/M`, `now/w`, `now-30d`, or `now-1M`; `M` is month and `m`
is minute. Absolute anchors and future starts are rejected.

Captain records the concrete rule groups selected for each model call and can
derive their completed `captain_model_calls` spend for inspection. Rules in this
foundation are observe-only: coverage, grouping, amount, and window state do
not change whether a chat request proceeds.
