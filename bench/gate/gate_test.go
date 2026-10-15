package main

import "testing"

const sample = `goos: darwin
BenchmarkGoSafClosedSuccess-10      	14000000	        70.0 ns/op	       0 B/op	       0 allocs/op
BenchmarkGoSafClosedSuccess-10      	14000000	        72.0 ns/op	       0 B/op	       0 allocs/op
BenchmarkGoSafClosedSuccess-10      	14000000	       500.0 ns/op	       0 B/op	       0 allocs/op
BenchmarkGobreakerOpenReject-10     	30000000	        38.0 ns/op	       0 B/op	       0 allocs/op
PASS
`

func TestParseTakesMedianAndStripsCPUSuffix(t *testing.T) {
	res := parse(sample)
	if got := res["BenchmarkGoSafClosedSuccess"]; got.nsOp != 72 || got.allocsOp != 0 {
		t.Fatalf("GoSaf = %+v, want median 72 ns/op (outlier ignored)", got)
	}
	if got := res["BenchmarkGobreakerOpenReject"]; got.nsOp != 38 {
		t.Fatalf("gobreaker = %+v, want 38 ns/op", got)
	}
}

func TestVerdict(t *testing.T) {
	gb := result{nsOp: 100, allocsOp: 0}
	tests := []struct {
		name string
		gs   result
		want bool
	}{
		{"faster", result{90, 0}, true},
		{"within tolerance", result{101.9, 0}, true},
		{"just over tolerance", result{102.1, 0}, false},
		{"more allocations", result{80, 1}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if ok, why := verdict(tt.gs, gb); ok != tt.want {
				t.Fatalf("verdict = %v (%s), want %v", ok, why, tt.want)
			}
		})
	}
}
