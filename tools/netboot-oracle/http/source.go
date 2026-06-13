package http

// The firmware source for the i100 downloader (q15 option c).
//
// cdn.githubraw.com is a plain-HTTP, commit-SHA-addressable proxy for
// raw.githubusercontent.com: it serves a repo's raw blob content at
//
//	http://cdn.githubraw.com/<owner>/<repo>/<sha>/<path>
//
// which lets the SAM fetch an exact pinned firmware revision over plain HTTP —
// no on-SAM TLS (i88 stays a lowest-priority fallback). The proxy is untrusted,
// so integrity comes separately from the SHA-256 verify (sha256.asm +
// tcp_conn.asm's conn_verify_*). See docs/specs/phase3-delivery-design.md §7 and
// item i100. The Z80 port is src/netboot/fw_source.asm.
const GithubRawHost = "cdn.githubraw.com"

// GithubRawPath builds the request path for a commit-pinned file on
// cdn.githubraw.com: /<owner>/<repo>/<sha>/<path>. owner/repo/sha pin the exact
// revision; path is the in-repo path (e.g. "boot/start4.elf"). The Z80
// fw_build_path (src/netboot/fw_source.asm) mirrors this byte-for-byte.
func GithubRawPath(owner, repo, sha, path string) string {
	return "/" + owner + "/" + repo + "/" + sha + "/" + path
}
