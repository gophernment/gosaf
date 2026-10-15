// Command gate runs the GoSaf vs gobreaker benchmarks and fails if GoSaf is
// slower or allocates more on the hot paths. Run it before every release:
//
//	cd bench && go run ./gate
package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
)

// tolerance is how much slower (as a fraction) GoSaf may be before the gate fails.
const tolerance = 0.02

type result struct {
	nsOp     float64
	allocsOp float64
}

// pair names the GoSaf and gobreaker benchmarks that must be compared.
type pair struct{ label, gosaf, gobreaker string }

var pairs = []pair{
	{"Closed, success", "BenchmarkGoSafClosedSuccess", "BenchmarkGobreakerClosedSuccess"},
	{"Open, reject", "BenchmarkGoSafOpenReject", "BenchmarkGobreakerOpenReject"},
}

// parse reads `go test -bench -benchmem` output and returns the median ns/op
// and allocs/op per benchmark name (the -N CPU suffix is removed).
func parse(output string) map[string]result {
	ns := map[string][]float64{}
	allocs := map[string][]float64{}
	sc := bufio.NewScanner(strings.NewReader(output))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 4 || !strings.HasPrefix(f[0], "Benchmark") {
			continue
		}
		name := f[0]
		if i := strings.LastIndex(name, "-"); i > 0 {
			name = name[:i]
		}
		for i := 2; i+1 < len(f); i++ {
			v, err := strconv.ParseFloat(f[i], 64)
			if err != nil {
				continue
			}
			switch f[i+1] {
			case "ns/op":
				ns[name] = append(ns[name], v)
			case "allocs/op":
				allocs[name] = append(allocs[name], v)
			}
		}
	}
	out := map[string]result{}
	for name, v := range ns {
		out[name] = result{nsOp: median(v), allocsOp: median(allocs[name])}
	}
	return out
}

func median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := slices.Clone(v)
	slices.Sort(s)
	if len(s)%2 == 1 {
		return s[len(s)/2]
	}
	return (s[len(s)/2-1] + s[len(s)/2]) / 2
}

// verdict reports whether GoSaf passes against gobreaker and why not.
func verdict(gs, gb result) (bool, string) {
	var why []string
	if gs.nsOp > gb.nsOp*(1+tolerance) {
		why = append(why, fmt.Sprintf("slower by %.1f%% (limit %.0f%%)", (gs.nsOp/gb.nsOp-1)*100, tolerance*100))
	}
	if gs.allocsOp > gb.allocsOp {
		why = append(why, fmt.Sprintf("%.0f allocs/op vs %.0f", gs.allocsOp, gb.allocsOp))
	}
	return len(why) == 0, strings.Join(why, "; ")
}

func main() {
	cmd := exec.Command("go", "test", "-run=^$", "-bench=ClosedSuccess$|OpenReject$",
		"-benchmem", "-count=10", "-benchtime=300ms", ".")
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		fmt.Fprintln(os.Stderr, "benchmarks failed:", err)
		os.Exit(2)
	}

	res := parse(string(out))
	failed := false
	fmt.Printf("%-18s %12s %12s %8s   %s\n", "path", "GoSaf ns/op", "gobreaker", "allocs", "result")
	for _, p := range pairs {
		gs, okA := res[p.gosaf]
		gb, okB := res[p.gobreaker]
		if !okA || !okB {
			fmt.Fprintf(os.Stderr, "missing benchmark results for %s\n", p.label)
			os.Exit(2)
		}
		ok, why := verdict(gs, gb)
		status := "PASS"
		if !ok {
			status, failed = "FAIL: "+why, true
		}
		fmt.Printf("%-18s %12.1f %12.1f %3.0f/%-3.0f   %s\n", p.label, gs.nsOp, gb.nsOp, gs.allocsOp, gb.allocsOp, status)
	}
	if failed {
		os.Exit(1)
	}
}
