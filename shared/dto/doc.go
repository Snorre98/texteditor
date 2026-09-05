// Package dto is the neutral, owner-free shared-DTO package (ADR-0027).
//
// Every type here is a pure DTO — plain data with no behavior, no methods, no
// channels, and no pointers into another module's live state. These are the only
// types that may appear in a module's public API signature (ADR-0016, ADR-0027).
// No module owns this package; modules import it. Composition of other pure DTOs
// is permitted (ADR-0027 §1).
//
// The catalog is pinned by contracts/interface.md §0 and §0b. Do not add methods
// to any type here, and do not define a boundary type here that embeds a sibling
// module's package type.
package dto

import "encoding/json"

// JSONSchema is an unparsed JSON Schema, spliced verbatim into the payload
// (function/parameters schemas); its size is metered (ADR-0011, ADR-0019).
type JSONSchema = json.RawMessage
