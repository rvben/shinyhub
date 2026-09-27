package wsdeflate

import "testing"

func TestNegotiate(t *testing.T) {
	const base = "permessage-deflate; server_no_context_takeover; client_no_context_takeover"
	cases := []struct {
		name   string
		offers []string
		want   string
		ok     bool
	}{
		{"browser default", []string{"permessage-deflate; client_max_window_bits"}, base, true},
		{"bare offer", []string{"permessage-deflate"}, base, true},
		{"case-insensitive name", []string{"PerMessage-Deflate"}, base, true},
		{"client limits its own window", []string{"permessage-deflate; client_max_window_bits=10"}, base, true},
		{"quoted client window", []string{`permessage-deflate; client_max_window_bits="12"`}, base, true},
		{"both no-context params", []string{"permessage-deflate; server_no_context_takeover; client_no_context_takeover"}, base, true},
		{"server window 15 is echoed", []string{"permessage-deflate; server_max_window_bits=15"}, base + "; server_max_window_bits=15", true},
		{"server window below 15 declined", []string{"permessage-deflate; server_max_window_bits=10"}, "", false},
		{"server window without value declined", []string{"permessage-deflate; server_max_window_bits"}, "", false},
		{"falls back to next acceptable offer", []string{"permessage-deflate; server_max_window_bits=9, permessage-deflate"}, base, true},
		{"offer in second header value", []string{"x-webkit-deflate-frame", "permessage-deflate"}, base, true},
		{"unknown parameter declined", []string{"permessage-deflate; mystery"}, "", false},
		{"duplicate parameter declined", []string{"permessage-deflate; client_no_context_takeover; client_no_context_takeover"}, "", false},
		{"no-context param with value declined", []string{"permessage-deflate; server_no_context_takeover=1"}, "", false},
		{"client window out of range", []string{"permessage-deflate; client_max_window_bits=16"}, "", false},
		{"client window below range", []string{"permessage-deflate; client_max_window_bits=7"}, "", false},
		{"client window leading zero", []string{"permessage-deflate; client_max_window_bits=09"}, "", false},
		{"other extension only", []string{"x-webkit-deflate-frame"}, "", false},
		{"no offers", nil, "", false},
		{"empty header", []string{""}, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := Negotiate(tc.offers)
			if ok != tc.ok || got != tc.want {
				t.Fatalf("Negotiate(%q) = %q, %v; want %q, %v", tc.offers, got, ok, tc.want, tc.ok)
			}
		})
	}
}
