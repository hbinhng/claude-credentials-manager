package cmd

import (
	"reflect"
	"testing"
)

func TestSplitCommaArgs(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"empty", []string{}, []string{}},
		{"single space-separated", []string{"alice"}, []string{"alice"}},
		{"two space-separated", []string{"alice", "bob"}, []string{"alice", "bob"}},
		{"single comma-separated", []string{"alice,bob"}, []string{"alice", "bob"}},
		{"three comma-separated", []string{"a,b,c"}, []string{"a", "b", "c"}},
		{"mixed", []string{"a,b", "c", "d,e"}, []string{"a", "b", "c", "d", "e"}},
		{"trailing comma", []string{"a,b,"}, []string{"a", "b"}},
		{"leading comma", []string{",a,b"}, []string{"a", "b"}},
		{"empty fragments", []string{"a,,b"}, []string{"a", "b"}},
		{"only commas", []string{",,,"}, []string{}},
		{"single empty arg", []string{""}, []string{}},
		{"uuid-prefix", []string{"1ba30dff,72824509"}, []string{"1ba30dff", "72824509"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := splitCommaArgs(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}
