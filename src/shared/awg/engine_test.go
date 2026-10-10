package awg

import (
	"strings"
	"testing"
)

func TestEngineCounters(t *testing.T) {
	value, err := parseTraffic("private_key=SECRET\nlast_handshake_time_sec=1700000000\nlast_handshake_time_nsec=123\nrx_bytes=99\ntx_bytes=42\n", "192.0.2.1:51820")
	if err != nil || value.Upload != 42 || value.Download != 99 || value.Handshake.UnixNano() != 1700000000000000123 {
		t.Fatalf("counters: %+v %v", value, err)
	}
	for _, text := range []string{"rx_bytes=-1", "last_handshake_time_sec=-1", "last_handshake_time_nsec=1000000000", "tx_bytes=SECRET"} {
		if _, err := parseTraffic(text, ""); err == nil || strings.Contains(err.Error(), "SECRET") {
			t.Fatal("invalid counters accepted or leaked", err)
		}
	}
}
