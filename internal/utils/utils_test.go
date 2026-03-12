package utils

import "testing"

func TestExtractTitleRoundTripSimple(t *testing.T) {
	encoded, err := EncodeTitle("Buy milk")
	if err != nil {
		t.Fatalf("EncodeTitle: %v", err)
	}
	got := ExtractTitle(encoded)
	if got != "Buy milk" {
		t.Fatalf("ExtractTitle mismatch: got %q", got)
	}
}

func TestExtractTitleRoundTripMultiline(t *testing.T) {
	want := "Project: test\n\nBody line\n\nhttps://www.notion.so/Test-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	encoded, err := EncodeTitle(want)
	if err != nil {
		t.Fatalf("EncodeTitle: %v", err)
	}
	got := ExtractTitle(encoded)
	if got != want {
		t.Fatalf("ExtractTitle multiline mismatch:\nwant: %q\ngot:  %q", want, got)
	}
}

func TestExtractTitleRoundTripUnicode(t *testing.T) {
	want := "Réunion\n\nSão Paulo → Belo AI"
	encoded, err := EncodeTitle(want)
	if err != nil {
		t.Fatalf("EncodeTitle: %v", err)
	}
	got := ExtractTitle(encoded)
	if got != want {
		t.Fatalf("ExtractTitle unicode mismatch:\nwant: %q\ngot:  %q", want, got)
	}
}

func TestExtractTitlePlainUTF8(t *testing.T) {
	got := ExtractTitle("U2luZ2xlIGxpbmU=")
	if got != "Single line" {
		t.Fatalf("ExtractTitle plain mismatch: got %q", got)
	}
}

func TestExtractTitleMalformedPayload(t *testing.T) {
	got := ExtractTitle("bm90LWEtZ3ppcC1wcm90bw==")
	if got != "not-a-gzip-proto" {
		t.Fatalf("ExtractTitle malformed fallback mismatch: got %q", got)
	}
}
