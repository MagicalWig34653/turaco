package views

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strconv"
	"time"
)

// CacheTTL is how long a derived per-viewer result of a View (a sidebar count, Q-C) may be reused.
const CacheTTL = 15 * time.Second

// CacheKey is the key under which a derived result of a View (for example a capped sidebar count) may be cached.
// A cached value is only ever valid for the exact combination it was computed for, so the key contains every input
// that can change what a viewer is allowed to see or what the View selects:
//
//   - the View id and its definition version (every edit, rename, share change, archive or take-over bumps the
//     version, so a changed or unshared View can never be answered from an older entry);
//   - the principal id and a fingerprint of the principal's permissions (a lost permission changes the key);
//   - fingerprints of the Team and role membership (a membership change changes the key);
//   - extra scope components of the owning module, for example the set of visible Queues.
//
// Fingerprints are order-independent and length-prefixed, so ["ab","c"] and ["a","bc"] differ. Because every input
// is part of the key, invalidation on revocation, edit or membership change needs no explicit purge; the TTL only
// bounds staleness caused by data changes the key cannot see (the rows themselves).
func CacheKey(viewID string, version int, principalID string, permissions, teamIDs, roleIDs []string, extra ...string) string {
	h := sha256.New()
	put := func(s string) {
		h.Write([]byte(strconv.Itoa(len(s))))
		h.Write([]byte{':'})
		h.Write([]byte(s))
	}
	put("views.cache.v1")
	put(viewID)
	put(strconv.Itoa(version))
	put(principalID)
	put(Fingerprint(permissions...))
	put(Fingerprint(teamIDs...))
	put(Fingerprint(roleIDs...))
	for _, e := range extra {
		put(e)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Fingerprint is an order-independent digest of a set of strings.
func Fingerprint(items ...string) string {
	sorted := slices.Clone(items)
	slices.Sort(sorted)
	sorted = slices.Compact(sorted)
	h := sha256.New()
	for _, s := range sorted {
		h.Write([]byte(strconv.Itoa(len(s))))
		h.Write([]byte{':'})
		h.Write([]byte(s))
	}
	return hex.EncodeToString(h.Sum(nil))[:32]
}
