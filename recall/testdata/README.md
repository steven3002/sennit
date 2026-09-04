# The recall regression corpus

Everything in `corpus.json` is **invented**. "Tidepool" is a fictional project,
the people are fictional, and no statement here is a fact about anything real.
That is deliberate: the corpus is committed publicly, so it cannot contain vault
content, and it cannot be scraped text either.

## What it is for

`recall` is the part of this system that can degrade without anything failing.
A model change, a field-policy change, a change to how the filter is weighted,
none of them break a test, and all of them can quietly make the right record
stop coming back. This corpus exists so that shows up as a number.

## Its shape, and why

The corpus is deliberately **imbalanced**: 70 of its 82 records belong to one
subject and the remaining 12 are a long tail. That is the vault shape a
real user has, and it is the one that was never measured until it was, a
single-domain vault and a mixed vault turn out to behave differently even at
matched neighbour density, so the harness reports **both** axes:

- **near-neighbour density per query**, how many records compete with a typical
  correct answer to that query. Recall falls steeply with it, and an aggregate
  score hides that completely: in the measured case one vault held two
  populations twenty-four points apart.
- **vault shape**, `dominant` or `long-tail`, because density alone does not
  account for the difference between them. A same-subject neighbour carries the
  same tags as the answer, so the filter cannot separate them; a neighbour from
  another subject at the same similarity is removed.

## Fields

| Field | Meaning |
|---|---|
| `id` | stable handle, used by the queries' gold sets |
| `type` | one of the six-type vocabulary |
| `statement` | one atomic proposition |
| `context` | what makes the statement resolvable on its own, mandatory |
| `tags` | what a writer would plausibly have tagged it |
| `shape` | `dominant` or `long-tail`, the sub-vault it belongs to |
| `supersedes` | the record this one replaces, where one does |

Queries carry a `gold` set and the `filter` a competent agent would emit **from
the query text alone**, never from the gold set, which would make the filter
arm measure nothing.

## Granularity

Twenty answerable queries means one query is worth five points of hit@5. The
harness is built to catch a regression, which is a change; it is not precise
enough to defend a level to three decimals, and nothing should quote it as if it
were.

## Running it

```sh
go test ./recall/...                                    # skips without a model
SENNIT_MODELS=/path/to/models go test ./recall/...    # runs, ~2 minutes
```

The harness never downloads the model. An ordinary `go test ./...` has to touch
no network, so a run without one **skips and stays green**; continuous
integration fetches the model as a setup step and points `SENNIT_MODELS` at it.

The corpus is embedded **once for the whole package**. Producing 82 vectors costs
about thirty seconds on the pure-Go backend, and rebuilding per test pushed the
package past Go's default ten-minute timeout, which would have made the harness
fail in exactly the place it exists to run.
