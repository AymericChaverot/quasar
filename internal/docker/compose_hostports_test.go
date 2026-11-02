package docker

import (
	"reflect"
	"testing"
)

func TestHostPortsInReadsWhatAStackBinds(t *testing.T) {
	cases := []struct {
		name    string
		compose string
		env     map[string]string
		want    []HostPort
	}{
		{
			name:    "the short syntax",
			compose: "services:\n  mc:\n    ports:\n      - \"25565:25565\"\n",
			want:    []HostPort{{Service: "mc", Lo: 25565, Hi: 25565, Proto: "tcp"}},
		},
		{
			// The whole point for a parameterised entry: the port is a variable
			// the operator picked, and reading the file literally would report
			// the wrong number or none at all.
			name:    "a port coming from the env",
			compose: "services:\n  mc:\n    ports:\n      - \"${HOST_PORT}:19132/udp\"\n",
			env:     map[string]string{"HOST_PORT": "19133"},
			want:    []HostPort{{Service: "mc", Lo: 19133, Hi: 19133, Proto: "udp"}},
		},
		{
			name:    "the long syntax",
			compose: "services:\n  db:\n    ports:\n      - target: 5432\n        published: \"5433\"\n        host_ip: 127.0.0.1\n        protocol: tcp\n",
			want:    []HostPort{{Service: "db", IP: "127.0.0.1", Lo: 5433, Hi: 5433, Proto: "tcp"}},
		},
		{
			name:    "an address in front of the port",
			compose: "services:\n  db:\n    ports:\n      - \"127.0.0.1:5432:5432\"\n",
			want:    []HostPort{{Service: "db", IP: "127.0.0.1", Lo: 5432, Hi: 5432, Proto: "tcp"}},
		},
		{
			name:    "an IPv6 address, which holds colons of its own",
			compose: "services:\n  db:\n    ports:\n      - \"[::1]:5432:5432\"\n",
			want:    []HostPort{{Service: "db", IP: "::1", Lo: 5432, Hi: 5432, Proto: "tcp"}},
		},
		{
			name:    "a range, kept whole",
			compose: "services:\n  v:\n    ports:\n      - \"2456-2458:2456-2458/udp\"\n",
			want:    []HostPort{{Service: "v", Lo: 2456, Hi: 2458, Proto: "udp"}},
		},
		{
			// Traefik holds them, and the adaptation drops them: they are never
			// where the stack is reached.
			name:    "the edge ports, left out",
			compose: "services:\n  web:\n    ports:\n      - \"80:80\"\n      - \"8443:443\"\n",
			want:    []HostPort{{Service: "web", Lo: 8443, Hi: 8443, Proto: "tcp"}},
		},
		{
			name:    "a container port published on whatever is free",
			compose: "services:\n  app:\n    ports:\n      - \"8080\"\n",
			want:    nil,
		},
		{
			name:    "a web app, which binds nothing",
			compose: "services:\n  app:\n    image: nginx\n",
			want:    nil,
		},
		{
			name:    "something that is not a compose file",
			compose: "ports: [oh dear",
			want:    nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := HostPortsIn(tc.compose, tc.env); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestHostPortClashesOnlyWhenTheDaemonWouldRefuse(t *testing.T) {
	udp := HostPort{Lo: 19132, Hi: 19132, Proto: "udp"}
	cases := []struct {
		name string
		a, b HostPort
		want bool
	}{
		{"the same port and protocol", udp, udp, true},
		{"the same number over TCP", udp, HostPort{Lo: 19132, Hi: 19132, Proto: "tcp"}, false},
		{"inside a range", HostPort{Lo: 2456, Hi: 2458, Proto: "udp"}, HostPort{Lo: 2457, Hi: 2457, Proto: "udp"}, true},
		{"next to a range", HostPort{Lo: 2456, Hi: 2458, Proto: "udp"}, HostPort{Lo: 2459, Hi: 2459, Proto: "udp"}, false},
		{"two different addresses",
			HostPort{IP: "127.0.0.1", Lo: 5432, Hi: 5432, Proto: "tcp"},
			HostPort{IP: "10.0.0.2", Lo: 5432, Hi: 5432, Proto: "tcp"}, false},
		{"an address and every address",
			HostPort{IP: "127.0.0.1", Lo: 5432, Hi: 5432, Proto: "tcp"},
			HostPort{Lo: 5432, Hi: 5432, Proto: "tcp"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.a.Clashes(tc.b); got != tc.want {
				t.Errorf("Clashes = %v, want %v", got, tc.want)
			}
			if got := tc.b.Clashes(tc.a); got != tc.want {
				t.Errorf("Clashes the other way = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestHostPortAddressIsWhatSomebodyTypes(t *testing.T) {
	cases := []struct {
		p          HostPort
		addr, dial string
	}{
		{HostPort{Lo: 2456, Hi: 2458, Proto: "udp"}, "v.example.com:2456-2458", "v.example.com:2456"},
		{HostPort{IP: "0.0.0.0", Lo: 25565, Hi: 25565}, "v.example.com:25565", "v.example.com:25565"},
		{HostPort{IP: "127.0.0.1", Lo: 5432, Hi: 5432}, "127.0.0.1:5432", "127.0.0.1:5432"},
		{HostPort{IP: "::1", Lo: 5432, Hi: 5432}, "[::1]:5432", "[::1]:5432"},
	}
	for _, tc := range cases {
		if got := tc.p.Address("v.example.com"); got != tc.addr {
			t.Errorf("Address = %q, want %q", got, tc.addr)
		}
		if got := tc.p.Dial("v.example.com"); got != tc.dial {
			t.Errorf("Dial = %q, want %q", got, tc.dial)
		}
	}
}
