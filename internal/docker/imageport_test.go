package docker

import "testing"

func TestServedPort(t *testing.T) {
	tests := []struct {
		name       string
		configured int
		exposed    []int
		want       int
		wantWhy    bool
	}{
		{
			name:       "image exposes nothing, the configured port stands",
			configured: 80,
			exposed:    nil,
			want:       80,
		},
		{
			name:       "image exposes the configured port",
			configured: 8080,
			exposed:    []int{8080},
			want:       8080,
		},
		{
			name:       "configured port among several exposed is a real choice",
			configured: 9090,
			exposed:    []int{80, 9090},
			want:       9090,
		},
		{
			// The case this exists for: a repository that swapped nginx serving
			// a built site for a server of its own, and moved 80 -> 4321.
			name:       "the only exposed port overrules a configured one the image cannot serve",
			configured: 80,
			exposed:    []int{4321},
			want:       4321,
			wantWhy:    true,
		},
		{
			name:       "several exposed and none configured is not guessed at",
			configured: 80,
			exposed:    []int{3000, 9090},
			want:       80,
			wantWhy:    true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, why := servedPort(tc.configured, tc.exposed)
			if got != tc.want {
				t.Errorf("servedPort(%d, %v) = %d, want %d", tc.configured, tc.exposed, got, tc.want)
			}
			if (why != "") != tc.wantWhy {
				t.Errorf("servedPort(%d, %v) explanation = %q, want explained: %v",
					tc.configured, tc.exposed, why, tc.wantWhy)
			}
		})
	}
}

func TestJoinInts(t *testing.T) {
	tests := []struct {
		in   []int
		want string
	}{
		{nil, ""},
		{[]int{80}, "80"},
		{[]int{80, 443}, "80 and 443"},
		{[]int{80, 3000, 9090}, "80, 3000 and 9090"},
	}
	for _, tc := range tests {
		if got := joinInts(tc.in); got != tc.want {
			t.Errorf("joinInts(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestLabelledPort(t *testing.T) {
	const key = "traefik.http.services.qs-abcd1234.loadbalancer.server.port"

	tests := []struct {
		name     string
		labels   map[string]string
		fallback int
		want     int
	}{
		{
			name:     "no labels at all falls back",
			labels:   nil,
			fallback: 80,
			want:     80,
		},
		{
			name:     "the label decides, not the app's configured port",
			labels:   map[string]string{key: "4321"},
			fallback: 80,
			want:     4321,
		},
		{
			name:     "other traefik labels are ignored",
			labels:   map[string]string{"traefik.enable": "true", key: "3000"},
			fallback: 80,
			want:     3000,
		},
		{
			name:     "an unreadable label falls back rather than probing port 0",
			labels:   map[string]string{key: "not-a-port"},
			fallback: 80,
			want:     80,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := labelledPort(tc.labels, tc.fallback); got != tc.want {
				t.Errorf("labelledPort(%v, %d) = %d, want %d", tc.labels, tc.fallback, got, tc.want)
			}
		})
	}
}
