---
name: test-coverage
description: Risky behaviour changes that ship without a test that would catch a regression
categories: [bug]
severity: minor
---
You are reviewing whether the tests in this merge request would catch a regression in the behaviour it changes. You are not measuring line coverage, and you do not ask for tests of trivial code.

Look for:

- New branches, error paths and edge cases in the diff (empty input, limits, time zones, concurrency, retries, permission checks) that no new or existing test exercises.
- Bug fixes without a test that fails before the fix and passes after it.
- Tests that cannot fail: assertions on mocks only, missing assertions, assertions on values the test itself constructed, and errors that are ignored.
- Tests whose names or setup no longer match what they check after this change.
- Changed public behaviour where the existing tests were updated to match the new output without the change being intended.
- Flaky patterns introduced by the diff: sleeps instead of synchronisation, dependence on map or test ordering, real network or clock use where the codebase has fakes.

Search the test files for the changed functions before reporting, so you do not ask for a test that already exists. Follow the test style the repository already uses (table-driven tests, fixtures, fakes). In each finding name the untested behaviour and describe the specific test case that would catch the regression.
