// http_main_test.go — host verification of the Z80 multi-file fetch-orchestration
// loop (src/netboot/http_main.asm), the port of the Go authority
// tools/netboot-oracle/http/provision.go::Provisioner. Built incrementally per
// docs/plans/z80-http-main-port-plan.md.
//
// Brick 1 (this file): prove the composition — http_main.asm pulls the single-
// file fetch machine, the pinned manifest + path builder, and the HTTP header
// skip into one binary with no label/org collisions, and every symbol the later
// bricks (prov_start / prov_first / prov_onframe / the store + verify wiring)
// compose must resolve. The behavioural prov_* tests land in the following bricks.
package z80_test

import (
	"os"
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

const (
	httpMainBinPath = "../../../build/netboot_http_main.bin"
	httpMainMapPath = "../../../build/netboot_http_main.map"
)

func loadHTTPMain(t *testing.T) *z80h.Machine {
	t.Helper()
	if _, err := os.Stat(httpMainBinPath); err != nil {
		t.Skipf("http_main binary not built (%s); run `make netboot-http-main`", httpMainBinPath)
	}
	mac, err := z80h.Load(httpMainBinPath, httpMainMapPath)
	if err != nil {
		t.Fatalf("load http_main: %v", err)
	}
	return mac
}

// TestHTTPMainComposes asserts the three include trees compose into one binary
// and every symbol the orchestration loop will compose from resolves — the fetch
// phase machine, the pinned manifest + per-file path builder, the header skip,
// and the streaming sink + SHA-256 verify path.
func TestHTTPMainComposes(t *testing.T) {
	mac := loadHTTPMain(t)
	for _, sym := range []string{
		// the Brick 1 placeholder entry
		"prov_skeleton",
		// the single-file fetch phase machine (netboot_http.asm)
		"http_fetch_first", "http_fetch_onframe",
		// the pinned manifest + the per-file path builder (fw_source.asm)
		"fw_plan_path", "fw_manifest_entry", "FW_PATH", "FW_MANIFEST",
		// (body_sink.asm's body_sink_write joins in Brick 3 — see the plan)
		// the streaming sink + the SHA-256 verify (tcp_conn.asm, NETBOOT_HOSTTEST)
		"CONN_SINK_ENABLED", "storage_sink_flush", "conn_verify_init", "conn_verify_final",
		"CONN_PINNED_HASH", "CONN_HASH_MATCH",
		// the SHA-256 primitive the verify drives (sha256.asm)
		"sha256_init", "sha256_update", "sha256_final",
	} {
		if _, err := mac.Sym(sym); err != nil {
			t.Errorf("symbol %q does not resolve in the composed http_main binary: %v", sym, err)
		}
	}
}
