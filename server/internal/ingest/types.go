package ingest

import "slices"

// knownTypes is the §4.2 event taxonomy. Every type is at `ver: 1`; ingest
// validates only that `ver` is ≥ 1, because §4.1 accepts and stores an
// unknown-but-higher version rather than rejecting the batch.
//
// This registry is a contract, not a convenience: §4.1 rejects a whole batch
// carrying an unknown `type`, on the grounds that the mod and the server ship
// together and version skew must be loud. Adding a type here means adding it to
// docs/events.md in the same change (and vice versa); the mod's Wire.cs mirrors
// the same list.
//
// Payload *shape* is deliberately not validated here. The projector (WP4) is
// the only consumer that needs to understand a payload, and unknown payload
// keys are preserved for forward compatibility (§4.1).
var knownTypes = []string{
	"session.started",
	"flight.started",
	"flight.ended",
	"flight.flagged",
	"vehicle.situation",
	"vehicle.atmosphere",
	"vehicle.orbit",
	"vehicle.soi",
	"vehicle.rud",
	"vehicle.impact",
	"vehicle.landed",
	"vehicle.staging",
	"vehicle.docked",
	"vehicle.undocked",
	"engine.ignition",
	"engine.shutdown",
	"engine.flameout",
	"kitten.eva_start",
	"kitten.eva_end",
	"kitten.tumble",
	"kitten.kia",
	"roster.snapshot",
	"telemetry.window",
}

// knownTypeSet is the lookup form, built once.
var knownTypeSet = func() map[string]struct{} {
	m := make(map[string]struct{}, len(knownTypes))
	for _, t := range knownTypes {
		m[t] = struct{}{}
	}
	return m
}()

// KnownType reports whether t is in the §4.2 taxonomy.
func KnownType(t string) bool {
	_, ok := knownTypeSet[t]
	return ok
}

// KnownTypes returns the taxonomy in registry order. Exported for the
// conformance vectors and for /admin/stats.
func KnownTypes() []string { return slices.Clone(knownTypes) }
