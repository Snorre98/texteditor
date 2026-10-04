# Methods

## Corpus construction

We assemble a corpus of long-form academic writing from the author's vault.
Documents are chunked on heading boundaries so that each retrieved passage
corresponds to a subsection rather than an arbitrary token window.

## Retrieval baseline

The baseline retrieves the top-k chunks by hybrid lexical and dense similarity
and injects all of them into the prompt. It makes no relevance judgment beyond
the retrieval score, so irrelevant but lexically similar passages are injected.

## Gated retrieval

The gated variant asks a typed-decision model whether each candidate passage is
directly relevant to the writing request, and injects only the passages it
keeps. The gate is applied after tray exclusions and before assembly; pinned
passages bypass it.
