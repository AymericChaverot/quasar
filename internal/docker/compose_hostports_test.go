package docker

import (
	"reflect"
	"testing"
)

func TestPublishedPortsReadsWhatAStackBinds(t *testing.T) {
	cases := []struct {
		name    string
		compose string
		env     map[string]string
		want    []int
	}{
		{
			name:    "the short syntax",
			compose: "services:\n  mc:\n    ports:\n      - \"25565:25565\"\n",
			want:    []int{25565},
		},
		{
			// The whole point for a parameterised entry: the port is a variable
			// the operator picked, and reading the file literally would report
			// the wrong number or none at all.
			name:    "a port coming from the env",
			compose: "services:\n  mc:\n    ports:\n      - \"${HOST_PORT}:25565\"\n",
			env:     map[string]string{"HOST_PORT": "25570"},
			want:    []int{25570},
		},
		{
			name:    "the long syntax",
			compose: "services:\n  db:\n    ports:\n      - target: 5432\n        published: \"5433\"\n",
			want:    []int{5433},
		},
		{
			name:    "a range, which binds every port in it",
			compose: "services:\n  v:\n    ports:\n      - \"2456-2458:2456-2458/udp\"\n",
			want:    []int{2456, 2457, 2458},
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
			if got := PublishedPorts(tc.compose, tc.env); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}
