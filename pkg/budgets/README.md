# Captain budget rules

Budget rules live only in Captain's database. Manage them from the Budgets page
in `captain serve`, with `captain budget-rule list|get|create|update|delete`, or
through `/api/v1/budget-rule`. A rule has this shape:

```json
{
  "name": "Team monthly budget",
  "match": {
    "dimensions": { "team": "platform-*" },
    "models": ["anthropic/claude-*"]
  },
  "groupBy": ["team"],
  "amount": 100,
  "window": "now/M"
}
```

Rule names are unique among live rules. Deleting a rule soft-deletes it: new
requests stop matching it, while the spend it already attributed keeps its
foreign key, and its name can be reused.

Amounts and settled-spend reports are USD. Windows are relative Elasticsearch
datemath such as `now/M`, `now/w`, `now-30d`, or `now-1M`; `M` is month and `m`
is minute. Absolute anchors and future starts are rejected.

Captain records the concrete rule groups selected for each model call and can
derive their completed `captain_model_calls` spend for inspection. Rules in this
foundation are observe-only: coverage, grouping, amount, and window state do
not change whether a chat request proceeds.
