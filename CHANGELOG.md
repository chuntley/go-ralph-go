# Changelog

## [0.6.1](https://github.com/chuntley/go-ralph-go/compare/v0.6.0...v0.6.1) (2026-07-22)


### Bug Fixes

* bound the refine-loop completion gate so it converges on capable models ([#27](https://github.com/chuntley/go-ralph-go/issues/27)) ([21c5b41](https://github.com/chuntley/go-ralph-go/commit/21c5b41a0d651b2c087458081c02c3dda055f121))

## [0.6.0](https://github.com/chuntley/go-ralph-go/compare/v0.5.0...v0.6.0) (2026-07-22)


### Features

* resilient refine passes and branch-resume on re-run ([#24](https://github.com/chuntley/go-ralph-go/issues/24)) ([b0ae095](https://github.com/chuntley/go-ralph-go/commit/b0ae095eafbfe4fb64d367885a4b98d0fecb9a69))

## [0.5.0](https://github.com/chuntley/go-ralph-go/compare/v0.4.0...v0.5.0) (2026-06-29)


### Features

* per-issue worktrees, parallel auto with live dashboard, and merge-repair ([#22](https://github.com/chuntley/go-ralph-go/pull/22))

## [0.4.0](https://github.com/chuntley/go-ralph-go/compare/v0.3.0...v0.4.0) (2026-06-17)


### Features

* open the PR deterministically and harden the cleanup pass ([#20](https://github.com/chuntley/go-ralph-go/issues/20)) ([02aa770](https://github.com/chuntley/go-ralph-go/commit/02aa770aff57227eb40c33b00e9850032b566518))

## [0.3.0](https://github.com/chuntley/go-ralph-go/compare/v0.2.2...v0.3.0) (2026-06-17)


### Features

* branch guardrail + auth preflight for safer auto runs ([#18](https://github.com/chuntley/go-ralph-go/issues/18)) ([828685d](https://github.com/chuntley/go-ralph-go/commit/828685d4a0aef2be88fcf14c9bcc19a75d68f3cc))

## [0.2.2](https://github.com/chuntley/go-ralph-go/compare/v0.2.1...v0.2.2) (2026-06-09)


### Bug Fixes

* halt `ralph auto` with an exit reason when an issue is marked failed ([#16](https://github.com/chuntley/go-ralph-go/issues/16)) ([3191cb2](https://github.com/chuntley/go-ralph-go/commit/3191cb2cca9b8bab85032996c4425cdeaeb7ce96))

## [0.2.1](https://github.com/chuntley/go-ralph-go/compare/v0.2.0...v0.2.1) (2026-06-04)


### Bug Fixes

* never let the plan file record an overall "complete" status ([#14](https://github.com/chuntley/go-ralph-go/issues/14)) ([902c847](https://github.com/chuntley/go-ralph-go/commit/902c847618fb8b62bfacc7771e0ea009f378d8bd))

## [0.2.0](https://github.com/chuntley/go-ralph-go/compare/v0.1.3...v0.2.0) (2026-06-03)


### Features

* goal-driven refine loop with min/max passes, verify gate, and auth reporting ([#12](https://github.com/chuntley/go-ralph-go/issues/12)) ([23ffab8](https://github.com/chuntley/go-ralph-go/commit/23ffab86b4ad50f880b5abc2834212430c0e84df))

## [0.1.3](https://github.com/chuntley/go-ralph-go/compare/v0.1.2...v0.1.3) (2026-05-12)


### Miscellaneous Chores

* release 0.1.3 ([688fb44](https://github.com/chuntley/go-ralph-go/commit/688fb44d698a66ba68b30744b9443e0e6b688551))

## [0.1.2](https://github.com/chuntley/go-ralph-go/compare/v0.1.1...v0.1.2) (2026-05-12)


### Bug Fixes

* redact credentials and tighten session-log permissions ([#5](https://github.com/chuntley/go-ralph-go/issues/5)) ([1babe59](https://github.com/chuntley/go-ralph-go/commit/1babe59856b1c1664ad5263868aa09a702b62c27))

## [0.1.1](https://github.com/chuntley/go-ralph-go/compare/v0.1.0...v0.1.1) (2026-05-11)


### Bug Fixes

* rotate Claude session per task and fix UTF-8 truncation ([5b5e8e4](https://github.com/chuntley/go-ralph-go/commit/5b5e8e467be167d289e462df21b11769f801730b))
* rotate Claude session per task and fix UTF-8 truncation ([08aa70e](https://github.com/chuntley/go-ralph-go/commit/08aa70e3b426e13a0c98a2d9d27b8045886d0d33))

## 0.1.0 (2026-05-11)


### Miscellaneous Chores

* bootstrap initial release ([0a59521](https://github.com/chuntley/go-ralph-go/commit/0a59521c2034bdd3eaaa5c829cd743d0c8609d21))
