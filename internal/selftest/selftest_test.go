package selftest

import (
	"context"
	"testing"
)

func TestRun(t *testing.T) {
	cs := Run(context.Background())
	if len(cs) != 3 {
		t.Fatalf("got %d checks", len(cs))
	}
	for _, c := range cs {
		if !c.OK {
			t.Errorf("%s: %s", c.Name, c.Detail)
		}
	}
}
