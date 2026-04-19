# Changelog

All notable changes to this project will be documented in this file.

## [Unreleased]

### Breaking Changes

- **Config:** `archive:` is now `archive.targets:` (a list of named targets).
  The previous single-block `archive:` structure is no longer valid.
- **Config:** `forward:` is a new top-level section with a `targets:` list.
  Forward targets replace the implicit domain `next_hop` delivery path for
  outbound relay; operators should configure explicit forward targets.
- **Queue:** Bucket names are now per-destination (`archive.<name>`,
  `forward.<name>`) rather than the fixed strings `forward` and `journal`.
  Existing queue databases from phase 1 are not compatible and should be
  drained before upgrading.

### Added

- Multiple archive destinations supported, each with an independent queue
  bucket, worker goroutine, and retry state.
- New `forward:` target type for plain SMTP delivery to external systems
  (e.g. EspoCRM, ERP). Each forward target has its own independent queue
  bucket and retry state.
- Per-destination dead-letter buckets (`dead.archive.<name>`,
  `dead.forward.<name>`).
- `queue.InitBuckets(names []string)` method for dynamic bucket creation
  at startup; adding a new target in config automatically creates its bucket
  at next startup.
- One delivery worker goroutine is spawned per configured destination,
  with bucket name included in log fields for clear observability.
