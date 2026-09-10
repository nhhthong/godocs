// Package id generates cryptographically secure random identifiers utilizing crypto/rand.
//
// Consolidating identifier generation here ensures uniform entropy and encoding across all
// domain layers (documents, users, session tokens), eliminating duplicate byte-generation logic.
package id

import (
	"crypto/rand"
	"encoding/hex"
)

// New generates a 128-bit cryptographically secure pseudorandom identifier encoded as a 32-character hex string.
// The collision probability remains negligible up to approximately 2^64 generated IDs (birthday paradox threshold).
// Suitable for entity primary keys (documents, users).
func New() string { return randHex(16) }

// Token generates a 256-bit cryptographically secure pseudorandom token encoded as a 64-character hex string.
// Intended for sensitive authentication tokens and session identifiers where brute-force resistance is paramount.
func Token() string { return randHex(32) }

func randHex(n int) string {
	b := make([]byte, n)
	// crypto/rand.Read draws entropy directly from the OS CSPRNG (getrandom(2) / /dev/urandom).
	// Under supported POSIX/Windows kernels, failure denotes catastrophic OS entropy exhaustion;
	// an immediate panic is the safest defensive response to prevent compromised security states.
	if _, err := rand.Read(b); err != nil {
		panic("id: crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b)
}
