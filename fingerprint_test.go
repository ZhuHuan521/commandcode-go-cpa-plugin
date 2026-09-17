package plugin

import "testing"

func TestFingerprintDeterministic(t *testing.T) {
	cfg := parseConfig([]byte(""))
	first := generateFingerprint("user_test_key", cfg)
	second := generateFingerprint("user_test_key", cfg)
	if mustJSON(first) != mustJSON(second) {
		t.Fatal("same key produced different fingerprints")
	}
	other := generateFingerprint("user_other_key", cfg)
	if mustJSON(first) == mustJSON(other) {
		t.Fatal("different keys produced identical fingerprints")
	}
	thumb := asString(first["thumbmark"])
	if len(thumb) != 64 {
		t.Fatalf("thumbmark = %q", thumb)
	}
}
