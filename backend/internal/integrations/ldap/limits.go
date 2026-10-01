package ldap

import ber "github.com/go-asn1-ber/asn1-ber"

// Resource limits against a misconfigured or hostile directory. A fetch that
// would exceed a limit fails as a whole: an incomplete snapshot is never
// returned, because the consumer would treat the missing objects as deleted.
const (
	// maxPacketLengthBytes bounds one LDAP message (one search result entry).
	// An AD range of 1500 members is about 150 KB; 16 MiB leaves room for an
	// OpenLDAP group with well over 100,000 members in a single entry.
	maxPacketLengthBytes = 16 << 20
)

// Variables so tests can inject small limits.
var (
	// maxUserEntries and maxGroupEntries bound the number of entries one
	// search may return.
	maxUserEntries  = 500_000
	maxGroupEntries = 200_000
)

// The go-asn1-ber library reads the packet size limit from a package-level
// variable and go-ldap offers no per-connection setting, so the limit is
// process-global: it applies to every go-ldap connection in the process. It
// is set once during package initialization, before any connection exists, so
// there is no concurrent access. The library default is 2 GiB, which lets a
// server make the client allocate that much memory with one packet header.
func init() {
	ber.MaxPacketLengthBytes = maxPacketLengthBytes
}
