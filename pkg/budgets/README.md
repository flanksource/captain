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

Captain records the concrete rule groups selected for each model call and derives
their settled `captain_model_calls` spend at admission. Once any rule exists,
every primary and fallback model must have coverage. Admission reserves the
positive resolved `budget.cost` against every rule/group bucket matched by the
selected model. Bucket locks make the settled-spend plus active-reservation
check atomic, and a multi-rule turn acquires every hold or none.

The hold remains attached to the turn across approval continuation and moves
atomically if fallback binding selects different buckets. As model calls settle,
their cost replaces the same amount of outstanding hold rather than counting
twice. Terminal completion, failure, or cancellation releases the unused hold.
Amounts remain USD-only; a completed non-USD call fails admission closed.

The reservation closes concurrent admission against the same balance, but it
does not make the run ceiling a provider-independent hard dollar cap. A single
model call can still exceed `budget.cost`, and its full settled cost is charged.
