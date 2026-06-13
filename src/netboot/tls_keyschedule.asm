; tls_keyschedule.asm — the TLS 1.3 key schedule (RFC 8446 §7.1), brick 1 of the
; handshake (docs/specs/tls13-handshake.md).  Pure orchestration over the built
; hkdf_extract (hkdf.asm) + expand_label (hkdf_expand_label.asm) — no new
; arithmetic.  Given the X25519 ECDHE shared secret and the two transcript hashes,
; it derives the full secret tree + each traffic secret's record key / iv /
; finished key:
;
;   Early      = HKDF-Extract(0, 0)
;   derived    = Derive-Secret(Early, "derived", "")
;   Handshake  = HKDF-Extract(derived, ECDHE)
;     c/s hs traffic = Derive-Secret(Handshake, "c/s hs traffic", Hash(CH..SH))
;   derived2   = Derive-Secret(Handshake, "derived", "")
;   Master     = HKDF-Extract(derived2, 0)
;     c/s ap traffic = Derive-Secret(Master, "c/s ap traffic", Hash(CH..serverFin))
;   per traffic secret: key=EL(s,"key","",32) iv=EL(s,"iv","",12) fin=EL(s,"finished","",32)
;
; where Derive-Secret(S,L,ctx) = HKDF-Expand-Label(S,L,ctx,32) and "" contexts for
; the two "derived" steps are Transcript-Hash("") = SHA-256("") (computed here),
; while the key/iv/finished contexts are the empty string (length 0).
;
; Three entry points: tls_key_schedule derives the whole tree in one shot (the
; brick-1 oracle, pinned to RFC 8448); tls_key_schedule_handshake +
; tls_key_schedule_application are the two-phase split the brick-6 state machine
; needs — the application transcript hash is only known after the server Finished,
; so the handshake secrets must derive first (ports of KeySchedule.DeriveHandshake
; / DeriveApplication, tools/netboot-oracle/tls/keyschedule.go). The one-shot entry
; is exactly the two phases run back-to-back.
;
; Built with -D NETBOOT_TLS_KS (NOT NETBOOT_STANDALONE), so the include chain's org
; guard (in sha256.asm, the base) stays inert and this file sets the org once — the
; primitives' own standalone builds are unchanged (the aead.asm composition idiom).
;
; AUTHORITY / VERIFICATION: host-verifiable in the project's standard way.
; tools/netboot-oracle/z80/tls_keyschedule_test.go assembles this file, runs
; tls_key_schedule under the koron-go/z80 harness, and asserts every output equals
; a Go RFC 8446 §7.1 reference (Go crypto/hkdf + the §7.1 HkdfLabel encoding) —
; itself anchored to the independent RFC 8448 known-answer (Early / derived /
; Handshake secrets), across two input sets — byte-for-byte. NOT host-verifiable:
; nothing here is hardware-gated.

                if defined(NETBOOT_TLS_KS)
                org     &8000
                endif

                include "hkdf_expand_label.asm" ; -> hkdf -> hmac -> sha256;
                                                ; provides hkdf_extract + expand_label

; ===========================================================================
; Inputs (the caller fills these before calling tls_key_schedule).
; ===========================================================================
KS_ECDHE:       defs 32                 ; in: the X25519 shared secret
KS_HASH_HS:     defs 32                 ; in: Transcript-Hash(ClientHello..ServerHello)
KS_HASH_AP:     defs 32                 ; in: Transcript-Hash(ClientHello..server Finished)

; ===========================================================================
; Outputs — the secret tree + per-traffic-secret record keys.
; ===========================================================================
KS_EARLY:       defs 32
KS_DERIVED:     defs 32
KS_HANDSHAKE:   defs 32
KS_DERIVED2:    defs 32
KS_MASTER:      defs 32
KS_CHS:         defs 32                 ; client handshake traffic secret
KS_SHS:         defs 32                 ; server handshake traffic secret
KS_CAP:         defs 32                 ; client application traffic secret
KS_SAP:         defs 32                 ; server application traffic secret

KS_CHS_KEY:     defs 32
KS_CHS_IV:      defs 12
KS_CHS_FIN:     defs 32
KS_SHS_KEY:     defs 32
KS_SHS_IV:      defs 12
KS_SHS_FIN:     defs 32
KS_CAP_KEY:     defs 32
KS_CAP_IV:      defs 12
KS_CAP_FIN:     defs 32
KS_SAP_KEY:     defs 32
KS_SAP_IV:      defs 12
KS_SAP_FIN:     defs 32

; ===========================================================================
; Scratch.
; ===========================================================================
KS_ZERO:        defs 32                 ; 32 zero bytes (the empty-extract salt/IKM)
KS_EMPTY_HASH:  defs 32                 ; SHA-256("") — the "" transcript context
ks_d_ctx:       defs 2                  ; ks_derive_secret: context pointer arg
ks_d_out:       defs 2                  ; ks_derive_secret: output pointer arg
ks_sec:         defs 2                  ; ks_traffic_keys: secret pointer
ks_ko:          defs 2                  ; ks_traffic_keys: key output pointer
ks_io:          defs 2                  ; ks_traffic_keys: iv output pointer
ks_fo:          defs 2                  ; ks_traffic_keys: finished output pointer

; The RFC 8446 §7.1 labels (without the "tls13 " prefix expand_label adds).
ks_l_derived:   defm "derived"          ; 7
ks_l_chs:       defm "c hs traffic"     ; 12
ks_l_shs:       defm "s hs traffic"     ; 12
ks_l_cap:       defm "c ap traffic"     ; 12
ks_l_sap:       defm "s ap traffic"     ; 12
ks_l_key:       defm "key"              ; 3
ks_l_iv:        defm "iv"               ; 2
ks_l_fin:       defm "finished"         ; 8

; ===========================================================================
; tls_key_schedule — the one-shot whole-tree derivation (the brick-1 oracle entry,
; pinned to RFC 8448 by the host test): the handshake secrets then the application
; secrets, from KS_ECDHE / KS_HASH_HS / KS_HASH_AP.  Identical output to the
; two-phase split below — the per-secret traffic-key derivations are independent of
; how they are grouped.  Clobbers A, BC, DE, HL, IX + all the hkdf/hmac/sha256
; scratch.
; ===========================================================================
tls_key_schedule:
                call    tls_key_schedule_handshake
                jp      tls_key_schedule_application

; ===========================================================================
; tls_key_schedule_handshake — port of KeySchedule.DeriveHandshake
; (tools/netboot-oracle/tls/keyschedule.go).  From KS_ECDHE + KS_HASH_HS derive
; Early / derived / Handshake, the client+server handshake-traffic secrets and
; their key/iv/finished, and derived2.  The two-phase state machine calls this
; after the ServerHello (hashHS known) — before the application hash exists.  Also
; establishes the shared KS_ZERO + KS_EMPTY_HASH scratch that the application phase
; reuses.  Clobbers A, BC, DE, HL, IX + scratch.
; ===========================================================================
tls_key_schedule_handshake:
                ; KS_ZERO = 32 zero bytes
                ld      hl, KS_ZERO
                ld      (hl), 0
                ld      de, KS_ZERO + 1
                ld      bc, 31
                ldir
                ; KS_EMPTY_HASH = SHA-256("")
                call    sha256_init
                ld      hl, KS_EMPTY_HASH
                call    sha256_final

                ; Early = HKDF-Extract(salt=0, IKM=0)
                ld      hl, KS_ZERO
                ld      de, KS_ZERO
                call    ks_extract
                ld      de, KS_EARLY
                call    ks_save_prk

                ; derived = Derive-Secret(Early, "derived", "")
                ld      hl, KS_EMPTY_HASH
                ld      (ks_d_ctx), hl
                ld      hl, KS_DERIVED
                ld      (ks_d_out), hl
                ld      hl, KS_EARLY
                ld      de, ks_l_derived
                ld      bc, 7
                call    ks_derive_secret

                ; Handshake = HKDF-Extract(salt=derived, IKM=ECDHE)
                ld      hl, KS_DERIVED
                ld      de, KS_ECDHE
                call    ks_extract
                ld      de, KS_HANDSHAKE
                call    ks_save_prk

                ; c hs traffic = Derive-Secret(Handshake, "c hs traffic", Hash(CH..SH))
                ld      hl, KS_HASH_HS
                ld      (ks_d_ctx), hl
                ld      hl, KS_CHS
                ld      (ks_d_out), hl
                ld      hl, KS_HANDSHAKE
                ld      de, ks_l_chs
                ld      bc, 12
                call    ks_derive_secret
                ; s hs traffic
                ld      hl, KS_HASH_HS
                ld      (ks_d_ctx), hl
                ld      hl, KS_SHS
                ld      (ks_d_out), hl
                ld      hl, KS_HANDSHAKE
                ld      de, ks_l_shs
                ld      bc, 12
                call    ks_derive_secret

                ; derived2 = Derive-Secret(Handshake, "derived", "")
                ld      hl, KS_EMPTY_HASH
                ld      (ks_d_ctx), hl
                ld      hl, KS_DERIVED2
                ld      (ks_d_out), hl
                ld      hl, KS_HANDSHAKE
                ld      de, ks_l_derived
                ld      bc, 7
                call    ks_derive_secret

                ; c hs traffic record key/iv/finished
                ld      hl, KS_CHS
                ld      (ks_sec), hl
                ld      hl, KS_CHS_KEY
                ld      (ks_ko), hl
                ld      hl, KS_CHS_IV
                ld      (ks_io), hl
                ld      hl, KS_CHS_FIN
                ld      (ks_fo), hl
                call    ks_traffic_keys
                ; s hs traffic record key/iv/finished
                ld      hl, KS_SHS
                ld      (ks_sec), hl
                ld      hl, KS_SHS_KEY
                ld      (ks_ko), hl
                ld      hl, KS_SHS_IV
                ld      (ks_io), hl
                ld      hl, KS_SHS_FIN
                ld      (ks_fo), hl
                jp      ks_traffic_keys         ; tail call returns to our caller

; ===========================================================================
; tls_key_schedule_application — port of KeySchedule.DeriveApplication.  From the
; derived2 left by tls_key_schedule_handshake + KS_HASH_AP derive Master and the
; client+server application-traffic secrets + their key/iv/finished.  REQUIRES a
; prior tls_key_schedule_handshake (it consumes KS_DERIVED2 and reuses KS_ZERO).
; Clobbers A, BC, DE, HL, IX + scratch.
; ===========================================================================
tls_key_schedule_application:
                ; Master = HKDF-Extract(salt=derived2, IKM=0)
                ld      hl, KS_DERIVED2
                ld      de, KS_ZERO
                call    ks_extract
                ld      de, KS_MASTER
                call    ks_save_prk

                ; c ap traffic = Derive-Secret(Master, "c ap traffic", Hash(CH..serverFin))
                ld      hl, KS_HASH_AP
                ld      (ks_d_ctx), hl
                ld      hl, KS_CAP
                ld      (ks_d_out), hl
                ld      hl, KS_MASTER
                ld      de, ks_l_cap
                ld      bc, 12
                call    ks_derive_secret
                ; s ap traffic
                ld      hl, KS_HASH_AP
                ld      (ks_d_ctx), hl
                ld      hl, KS_SAP
                ld      (ks_d_out), hl
                ld      hl, KS_MASTER
                ld      de, ks_l_sap
                ld      bc, 12
                call    ks_derive_secret

                ; c ap traffic record key/iv/finished
                ld      hl, KS_CAP
                ld      (ks_sec), hl
                ld      hl, KS_CAP_KEY
                ld      (ks_ko), hl
                ld      hl, KS_CAP_IV
                ld      (ks_io), hl
                ld      hl, KS_CAP_FIN
                ld      (ks_fo), hl
                call    ks_traffic_keys
                ; s ap traffic record key/iv/finished
                ld      hl, KS_SAP
                ld      (ks_sec), hl
                ld      hl, KS_SAP_KEY
                ld      (ks_ko), hl
                ld      hl, KS_SAP_IV
                ld      (ks_io), hl
                ld      hl, KS_SAP_FIN
                ld      (ks_fo), hl
                jp      ks_traffic_keys

; ---------------------------------------------------------------------------
; ks_extract — HKDF-Extract with 32-byte salt and 32-byte IKM into HKDF_PRK.
; In: HL = salt ptr, DE = IKM ptr.  Clobbers A, BC, DE, HL, IX + scratch.
; ---------------------------------------------------------------------------
ks_extract:
                ld      (HKDF_SALT_PTR), hl
                ld      (HKDF_IKM_PTR), de
                ld      hl, 32
                ld      (HKDF_SALT_LEN), hl
                ld      (HKDF_IKM_LEN), hl
                jp      hkdf_extract

; ks_save_prk — copy the 32-byte HKDF_PRK to (DE).  Clobbers BC, DE, HL.
ks_save_prk:
                ld      hl, HKDF_PRK
                ld      bc, 32
                ldir
                ret

; ---------------------------------------------------------------------------
; ks_derive_secret — Derive-Secret = Expand-Label(secret, label, ctx, 32).
; In:  HL = secret ptr, DE = label ptr, BC = label length; ks_d_ctx = the
; 32-byte transcript-hash context ptr, ks_d_out = the output ptr.
; Clobbers A, BC, DE, HL, IX + scratch.
; ---------------------------------------------------------------------------
ks_derive_secret:
                ld      (EL_SECRET_PTR), hl
                ld      (EL_LABEL_PTR), de
                ld      (EL_LABEL_LEN), bc
                ld      hl, (ks_d_ctx)
                ld      (EL_CTX_PTR), hl
                ld      hl, 32                  ; context = a 32-byte hash
                ld      (EL_CTX_LEN), hl
                ld      (EL_LENGTH), hl         ; output 32 bytes
                ld      hl, (ks_d_out)
                jp      expand_label

; ---------------------------------------------------------------------------
; ks_traffic_keys — from the traffic secret at (ks_sec): key -> (ks_ko, 32),
; iv -> (ks_io, 12), finished -> (ks_fo, 32), each Expand-Label with an empty
; context.  Clobbers A, BC, DE, HL, IX + scratch.
; ---------------------------------------------------------------------------
ks_traffic_keys:
                ld      hl, (ks_sec)            ; key (32)
                ld      (EL_SECRET_PTR), hl
                ld      hl, ks_l_key
                ld      (EL_LABEL_PTR), hl
                ld      hl, 3
                ld      (EL_LABEL_LEN), hl
                ld      hl, 0
                ld      (EL_CTX_LEN), hl
                ld      hl, 32
                ld      (EL_LENGTH), hl
                ld      hl, (ks_ko)
                call    expand_label

                ld      hl, (ks_sec)            ; iv (12)
                ld      (EL_SECRET_PTR), hl
                ld      hl, ks_l_iv
                ld      (EL_LABEL_PTR), hl
                ld      hl, 2
                ld      (EL_LABEL_LEN), hl
                ld      hl, 0
                ld      (EL_CTX_LEN), hl
                ld      hl, 12
                ld      (EL_LENGTH), hl
                ld      hl, (ks_io)
                call    expand_label

                ld      hl, (ks_sec)            ; finished (32)
                ld      (EL_SECRET_PTR), hl
                ld      hl, ks_l_fin
                ld      (EL_LABEL_PTR), hl
                ld      hl, 8
                ld      (EL_LABEL_LEN), hl
                ld      hl, 0
                ld      (EL_CTX_LEN), hl
                ld      hl, 32
                ld      (EL_LENGTH), hl
                ld      hl, (ks_fo)
                jp      expand_label
