# Attention mechanisms

## Scaled dot-product attention

Scaled dot-product attention computes a weighted sum of values, where the weight
of each value is the compatibility of its key with the query, divided by the
square root of the key dimension. The scaling counteracts the growth of the
dot product with dimension, keeping the softmax in a regime with useful
gradients.

## Multi-head attention

Multi-head attention runs several attention functions in parallel over
projected queries, keys, and values, then concatenates and re-projects the
outputs. Different heads can attend to different positions and subspaces, which
lets one layer represent several relations at once.

## Positional encodings

Because attention is permutation-invariant, sequence order must be injected.
Sinusoidal positional encodings add a fixed function of position; learned
encodings add a trained vector per position. Rotary encodings rotate the query
and key vectors by a position-dependent angle.
