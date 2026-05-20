package main

import (
	"bytes"
	"testing"
)

func TestCrlfToLf_Empty(t *testing.T) {
	got := crlfToLf(nil)
	if !bytes.Equal(got, []byte{}) && got != nil {
		t.Errorf("crlfToLf(nil): got %q, want empty", got)
	}
}

func TestCrlfToLf_NoCRLF(t *testing.T) {
	in := []byte("hello\nworld\n")
	got := crlfToLf(in)
	want := []byte("hello\nworld\n")
	if !bytes.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestCrlfToLf_BasicReplacement(t *testing.T) {
	in := []byte("line1\r\nline2\r\nline3\r\n")
	got := crlfToLf(in)
	want := []byte("line1\nline2\nline3\n")
	if !bytes.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestCrlfToLf_MixedEndings(t *testing.T) {
	// CRLF, lone LF, lone CR — only CRLF gets normalised.
	in := []byte("a\r\nb\nc\rd\r\n")
	got := crlfToLf(in)
	want := []byte("a\nb\nc\rd\n")
	if !bytes.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestCrlfToLf_OnlyCR(t *testing.T) {
	// Lone CR (no LF following) must be preserved verbatim.
	in := []byte("a\rb\rc")
	got := crlfToLf(in)
	want := []byte("a\rb\rc")
	if !bytes.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestCrlfToLf_TrailingCR(t *testing.T) {
	// Lone CR at end of input is not CRLF; keep it.
	in := []byte("hello\r")
	got := crlfToLf(in)
	want := []byte("hello\r")
	if !bytes.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}
