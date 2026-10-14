package circuit_test

import (
	"testing"

	"github.com/gophernment/gosaf/internal/circuit"
)

func TestPolicyTripped(t *testing.T) {
	p := circuit.Policy{MinCalls: 4, FailureRate: 0.5, SlowRate: 0.8}
	tests := []struct {
		name string
		c    circuit.Counts
		want bool
	}{
		{"below MinCalls even if all fail", circuit.Counts{Total: 3, Failures: 3}, false},
		{"at MinCalls, below rate", circuit.Counts{Total: 4, Failures: 1}, false},
		{"at MinCalls, rate exactly at threshold", circuit.Counts{Total: 4, Failures: 2}, true},
		{"slow rate trips", circuit.Counts{Total: 5, Slow: 4}, true},
		{"slow rate just below", circuit.Counts{Total: 10, Slow: 7}, false},
		{"empty", circuit.Counts{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := p.Tripped(tt.c); got != tt.want {
				t.Fatalf("Tripped(%+v) = %v, want %v", tt.c, got, tt.want)
			}
		})
	}
}

func TestPolicyUnhealthyIgnoresMinCalls(t *testing.T) {
	p := circuit.Policy{MinCalls: 100, FailureRate: 0.5, SlowRate: 1}
	if !p.Unhealthy(circuit.Counts{Total: 2, Failures: 1}) {
		t.Fatal("Unhealthy must not apply MinCalls")
	}
}
