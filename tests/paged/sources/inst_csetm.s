        .text
        // csetm Rd, cond — Conditional Set Mask (Mac-side oracle:
        // tools/refenc/pass2.go encodeCsetm, case 52).  csetm is an alias of
        // CSINV Rd, XZR, XZR, invert(cond): the condition field in the machine
        // code is cond XOR 1.  Grounded against aarch64-none-elf-as + ARM ARM
        // C6.2.58 (CSETM).  This fixture exercises both widths and several
        // conditions to confirm the SAM-side inversion matches GNU.
        csetm   w0, ne
        csetm   x1, eq
        csetm   w2, cs
        csetm   x3, lt
        csetm   w7, ne
        csetm   x24, ne
        csetm   w5, gt
        csetm   x6, le
