package utils

import (
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"
)

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

func TestStrToTsDateOnly(t *testing.T) {
	ts, err := StrToTs("2026-03-24")
	if err != nil {
		t.Fatalf("StrToTs date-only: %v", err)
	}
	got := time.UnixMilli(ts).In(time.Local).Format("2006-01-02")
	if got != "2026-03-24" {
		t.Fatalf("date-only mismatch: got %q", got)
	}
}

func TestStrToTsLocalDateTime(t *testing.T) {
	ts, err := StrToTs("2026-03-24T16:30")
	if err != nil {
		t.Fatalf("StrToTs local datetime: %v", err)
	}
	got := time.UnixMilli(ts).In(time.Local).Format("2006-01-02T15:04")
	if got != "2026-03-24T16:30" {
		t.Fatalf("local datetime mismatch: got %q", got)
	}
}

func TestStrToTsRFC3339(t *testing.T) {
	ts, err := StrToTs("2026-03-24T16:30-03:00")
	if err != nil {
		t.Fatalf("StrToTs rfc3339: %v", err)
	}
	got := time.UnixMilli(ts).In(time.FixedZone("x", -3*3600)).Format("2006-01-02T15:04Z07:00")
	if got != "2026-03-24T16:30-03:00" {
		t.Fatalf("rfc3339 mismatch: got %q", got)
	}
}

func TestParseDuePreservesTimeShape(t *testing.T) {
	dateOnly, err := ParseDue("2026-03-24")
	if err != nil {
		t.Fatalf("ParseDue date-only: %v", err)
	}
	if dateOnly.HasTime {
		t.Fatal("expected date-only due to report HasTime=false")
	}

	timed, err := ParseDue("2026-03-24T16:30")
	if err != nil {
		t.Fatalf("ParseDue timed: %v", err)
	}
	if !timed.HasTime {
		t.Fatal("expected timed due to report HasTime=true")
	}
}

func TestEncodeDateComponents(t *testing.T) {
	spec, err := ParseDue("2026-03-24T16:30")
	if err != nil {
		t.Fatalf("ParseDue: %v", err)
	}
	encoded, err := EncodeDateComponents(spec)
	if err != nil {
		t.Fatalf("EncodeDateComponents: %v", err)
	}

	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("base64 decode: %v", err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("json decode: %v", err)
	}

	if got := int(payload["year"].(float64)); got != 2026 {
		t.Fatalf("year mismatch: got %d", got)
	}
	if got := int(payload["month"].(float64)); got != 3 {
		t.Fatalf("month mismatch: got %d", got)
	}
	if got := int(payload["day"].(float64)); got != 24 {
		t.Fatalf("day mismatch: got %d", got)
	}
	if got := int(payload["hour"].(float64)); got != 16 {
		t.Fatalf("hour mismatch: got %d", got)
	}
	if got := int(payload["minute"].(float64)); got != 30 {
		t.Fatalf("minute mismatch: got %d", got)
	}
}

func TestTsToStrKeepsTimeWhenPresent(t *testing.T) {
	loc := time.FixedZone("x", -3*3600)
	ts := time.Date(2026, 3, 24, 16, 30, 0, 0, loc).UnixMilli()
	got := TsToStr(ts)
	if got == "2026-03-24" {
		t.Fatalf("expected datetime output, got %q", got)
	}
}
