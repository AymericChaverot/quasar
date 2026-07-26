package server

import "testing"

// A malformed entry has to be rejected at the form. Traefik discards a
// middleware it cannot parse, which fails open — the app would stay reachable
// by everyone while the UI showed an allowlist.
func TestNormalizeCIDR(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{in: "203.0.113.4", want: "203.0.113.4/32"},
		{in: "203.0.113.0/24", want: "203.0.113.0/24"},
		{in: "10.0.0.0/8", want: "10.0.0.0/8"},
		{in: "2001:db8::1", want: "2001:db8::1/128"},
		{in: "2001:db8::/32", want: "2001:db8::/32"},

		{in: "", wantErr: true},
		{in: "not-an-ip", wantErr: true},
		{in: "203.0.113.4/99", wantErr: true},
		{in: "203.0.113.300", wantErr: true},
		// A hostname is not an address: Traefik matches on addresses only, so
		// accepting this would silently allow nobody.
		{in: "office.example.com", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got, err := normalizeCIDR(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Errorf("normalizeCIDR(%q) = %q, expected an error", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("normalizeCIDR(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
