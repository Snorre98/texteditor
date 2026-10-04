# Evaluation

## Golden queries

Each golden query pairs a writing request with the set of corpus chunks that a
human judge considers relevant to the request. Relevance is judged at the
subheader level, matching the retrieval and gating granularity.

## Metrics

Recall@k measures the fraction of judged-relevant chunks that survive the
pipeline. Injected tokens measure the total prompt tokens contributed by
retrieved passages. Latency measures wall-clock time added by the decision
layer. The thesis is that gating reduces injected tokens without reducing
recall@k.

## Threshold tuning

The gate keep threshold tau trades recall against token savings: a lower tau
keeps more passages (higher recall, more tokens); a higher tau keeps fewer. The
shipped default is tuned on the golden set and recorded in status.md.
