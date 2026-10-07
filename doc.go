// Package systemone is a client for the System One API. POST /v1/systemone
// answers named noul, choice, and score questions about a piece of state;
// GET /v1/models lists the models a server offers.
//
// The types mirror the wire format and add no translation to other decision
// APIs. Any server that implements the format is reachable by setting its base
// URL.
package systemone
