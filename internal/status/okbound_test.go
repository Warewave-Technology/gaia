package status

import (
	"math"
	"testing"
	"time"
)

// OKBound is what Compute allows: in the best case (a test every T proving a point taken as it finishes, backups
// within the RPO target), the share of one test cycle that Compute says OK is max_ok_fraction, and with the suggested
// interval it is all of it.
func TestOKBound(t *testing.T) {
	okShare := func(tg Targets, T int64) float64 {
		const steps = 2000
		ok := 0
		t0 := now
		for i := range steps {
			x := time.Duration(float64(T) * float64(i) / steps * float64(time.Second))
			test := &Test{ID: 1, PointID: 1, Verdict: OK, PointTakenAt: t0, FinishedAt: t0, ProvenRTOS: f(10)}
			in := Input{Now: t0.Add(x), Targets: &tg, NewestChecked: &Point{ID: 2, TakenAt: t0.Add(x)}, LatestTest: test, LatestPassingTest: test}
			if Compute(in).Status == OK {
				ok++
			}
		}
		return float64(ok) / steps
	}
	for _, c := range []struct {
		rpo, age, interval int64
		want               float64
		suggest            int64 // 0: none
	}{
		{4 * 3600, 2 * 86400, 86400, 4.0 / 24, 4 * 3600}, // daily tests, 4 h RPO: at most 17 % of the day
		{86400, 3600, 7200, 0.5, 3600},                   // the proof age is the tighter one
		{3600, 86400, 1800, 1, 0},                        // tested often enough
		{3600, 3600, 3600, 1, 0},                         // exactly
		{900, 86400 * 7, 7 * 86400, 900.0 / 604800, 900}, // weekly tests, 15 min RPO
	} {
		tg := Targets{RPOS: c.rpo, RTOS: 600, MaxProofAgeS: c.age}
		got, sug := OKBound(tg, c.interval)
		if math.Abs(got-c.want) > 1e-9 || (sug == nil) != (c.suggest == 0) || sug != nil && *sug != c.suggest {
			t.Fatalf("%+v: OKBound %v %v", c, got, sug)
		}
		if share := okShare(tg, c.interval); math.Abs(share-got) > 0.002 {
			t.Fatalf("%+v: Compute is OK %.4f of the cycle, OKBound says %.4f", c, share, got)
		}
		if sug != nil {
			if again, s2 := OKBound(tg, *sug); again != 1 || s2 != nil {
				t.Fatalf("%+v: with the suggested interval %d: %v %v", c, *sug, again, s2)
			}
			if share := okShare(tg, *sug); share != 1 {
				t.Fatalf("%+v: with the suggested interval Compute is OK %.4f of the cycle", c, share)
			}
		}
	}
}
