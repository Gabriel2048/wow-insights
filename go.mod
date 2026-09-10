module wowinsight

// The language version. Keep this at minor granularity: a patch-level directive
// here forces a GOTOOLCHAIN download on any builder running an older patch.
go 1.26

// The floor the toolchain is pinned to. 1.26.8 is the first release on this
// line that clears the net/http advisories govulncheck reports against 1.26.4,
// one of which sits on the exact call path Client.Query uses.
toolchain go1.26.8
