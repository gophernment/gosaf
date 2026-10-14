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

func TestPolicyConsecutiveTripped(t *testing.T) {
	tests := []struct {
		name string
		p    circuit.Policy
		n    int
		want bool
	}{
		{"disabled never trips", circuit.Policy{}, 1000, false},
		{"below threshold", circuit.Policy{ConsecutiveFailures: 3}, 2, false},
		{"at threshold", circuit.Policy{ConsecutiveFailures: 3}, 3, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.p.ConsecutiveTripped(tt.n); got != tt.want {
				t.Fatalf("ConsecutiveTripped(%d) = %v, want %v", tt.n, got, tt.want)
			}
		})
	}
}

func TestPolicyDoomed(t *testing.T) {
	p := circuit.Policy{FailureRate: 0.5, SlowRate: 0.5}
	tests := []struct {
		name string
		c    circuit.Counts
		size int
		want bool
	}{
		{"one failure of four can still recover", circuit.Counts{Total: 1, Failures: 1}, 4, false},
		{"two failures of four cannot", circuit.Counts{Total: 2, Failures: 2}, 4, true},
		{"slow calls count too", circuit.Counts{Total: 2, Slow: 2}, 4, true},
		{"all probes done and healthy", circuit.Counts{Total: 4, Failures: 1}, 4, false},
		{"all probes done and unhealthy", circuit.Counts{Total: 4, Failures: 2}, 4, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := p.Doomed(tt.c, tt.size); got != tt.want {
				t.Fatalf("Doomed(%+v, %d) = %v, want %v", tt.c, tt.size, got, tt.want)
			}
		})
	}
}
