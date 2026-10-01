package config

import (
	"reflect"
	"testing"
)

func TestParseList(t *testing.T) {
	for _, c := range []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"1555196261286809712", []string{"1555196261286809712"}},
		{"1555196261286809712 1555278127331020933", []string{"1555196261286809712", "1555278127331020933"}},
		{" 1, 2,,3\n2\t1 ", []string{"1", "2", "3"}}, // any separator, repeats dropped, order kept
	} {
		if got := ParseList(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("ParseList(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
