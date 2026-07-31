// SATELLITE module: a pre-execution sandbox that rehearses a step on a REMOTE
// sandbox service over HTTP -- the shape a cloud sandbox such as E2B or Modal
// takes, where no container runtime is needed on the CEE host.
//
// Its own go.mod keeps any cloud-SDK dependency out of the core cee module.
// This reference talks the sandbox over a small HTTP contract using only the
// standard library, so it builds and tests offline; a variant built on a
// vendor's Go SDK (e.g. E2B) would carry that require HERE, never in the core.
module github.com/p0nymc1/cee/satellites/httpsandbox

go 1.26.5

require github.com/p0nymc1/cee v0.0.0

replace github.com/p0nymc1/cee => ../..
