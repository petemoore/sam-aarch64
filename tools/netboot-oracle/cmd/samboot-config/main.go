// Command samboot-config is the host-side "BIOS setup" editor for the SAMBOOT
// default-boot-record config (i176). It encodes a config into the 1 KB Trinity
// EEPROM chunk bytes that would be flashed, and decodes existing chunk bytes back
// to a human-readable config. The byte layout + chunk name are the samboot
// package (docs/specs/samboot.md §4); the matching Z80 reader is
// src/netboot/samboot_config.asm.
//
// This produces/reads the bytes only — it does NOT write to a real EEPROM. The
// flash of the chunk to hardware is the i135c path (via the Trinity write_chunk
// routine), inherently destructive and out of scope here.
//
// Usage:
//
//	# Encode an auto-boot config for record 3 to a 1 KB chunk file:
//	samboot-config encode --record 3 --out cfg.bin
//
//	# Encode a "no auto-boot" config (normal boot, wait for user):
//	samboot-config encode --none --out cfg.bin
//
//	# Decode an existing chunk file back to a human-readable config:
//	samboot-config decode --in cfg.bin
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/samboot"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "samboot-config:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return usageErr()
	}
	switch args[0] {
	case "encode":
		return runEncode(args[1:])
	case "decode":
		return runDecode(args[1:])
	case "-h", "--help", "help":
		printUsage(os.Stdout)
		return nil
	default:
		return fmt.Errorf("unknown subcommand %q", args[0])
	}
}

func runEncode(args []string) error {
	fs := flag.NewFlagSet("encode", flag.ContinueOnError)
	record := fs.Int("record", -1, "Trinity record number to auto-boot at power-on (0..65535)")
	none := fs.Bool("none", false, "encode a no-auto-boot config (normal boot, wait for user)")
	out := fs.String("out", "", "output file for the 1 KB chunk bytes (default: stdout)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	var cfg samboot.Config
	switch {
	case *none && *record >= 0:
		return fmt.Errorf("--none and --record are mutually exclusive")
	case *none:
		cfg = samboot.None()
	case *record >= 0:
		if *record > 0xFFFF {
			return fmt.Errorf("--record %d out of range (0..65535)", *record)
		}
		cfg = samboot.Boot(uint16(*record))
	default:
		return fmt.Errorf("provide --record N or --none")
	}

	data := cfg.Encode()
	if *out == "" || *out == "-" {
		_, err := os.Stdout.Write(data)
		return err
	}
	if err := os.WriteFile(*out, data, 0o644); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "wrote %d bytes (%s) for %q -> %s\n", len(data), cfg, samboot.ChunkName, *out)
	return nil
}

func runDecode(args []string) error {
	fs := flag.NewFlagSet("decode", flag.ContinueOnError)
	in := fs.String("in", "", "input file holding the 1 KB chunk bytes (default: stdin)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	var data []byte
	var err error
	if *in == "" || *in == "-" {
		data, err = io.ReadAll(os.Stdin)
	} else {
		data, err = os.ReadFile(*in)
	}
	if err != nil {
		return err
	}

	cfg, err := samboot.Decode(data)
	if err != nil {
		return err
	}
	fmt.Printf("chunk: %q\n%s\n", samboot.ChunkName, cfg)
	return nil
}

func usageErr() error {
	printUsage(os.Stderr)
	return fmt.Errorf("a subcommand is required")
}

func printUsage(w *os.File) {
	fmt.Fprint(w, `samboot-config — host-side BIOS setup editor for the SAMBOOT default-boot-record config

Subcommands:
  encode --record N [--out file]   encode an auto-boot config for record N
  encode --none      [--out file]  encode a no-auto-boot config (normal boot)
  decode [--in file]               decode chunk bytes to a human-readable config

The output of encode is the 1 KB EEPROM chunk bytes that would be flashed to the
"SAMBOOT Config  " Trinity chunk. This tool does NOT write to hardware (the flash
is the i135c path). See docs/specs/samboot.md §4.
`)
}
