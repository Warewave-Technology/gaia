package status

// OKBound is how much of the time a VM on a policy can be OK at best, and the test interval that would make OK
// possible all the time (ADR-007 C12; accepted as an upper bound in C17.3, which the screens say as "at most"). The
// status model does not change for it; this only says what the model allows.
//
// Derivation, from Compute: OK needs the theoretical RPO within the RPO target R, a passing test whose proven RTO is
// within the RTO target, proven RPO <= R and proof age <= A (the maximum proof age). Take the best case: backups
// keep the theoretical RPO within R, every test passes within the RTO target, tests finish exactly every T
// (test_interval_s), and each test proves a point taken the instant it finishes (proven RPO = proof age = 0 then;
// any real point is older, so this is an upper bound). x seconds after a test,
//
//	proven RPO = x   and   proof age = x
//
// so the VM is OK while x <= R and x <= A, until the next test at x = T resets both. In every cycle of T seconds it
// is OK for min(T, R, A) seconds:
//
//	max_ok_fraction           = min(1, min(R, A) / T)
//	suggested_test_interval_s = min(R, A)   when T > min(R, A); none otherwise (1 is already possible)
//
// With the suggested interval max_ok_fraction is 1. A real test proves a point that is already some minutes old (the
// backup before it, the restore and boot), so a VM tested exactly at the suggested interval still drops to WARNING
// for those minutes; a shorter interval leaves room for them. Targets or an interval of 0 or less say nothing: 1, nil.
func OKBound(t Targets, testIntervalS int64) (float64, *int64) {
	limit := min(t.RPOS, t.MaxProofAgeS)
	if testIntervalS <= 0 || limit <= 0 || testIntervalS <= limit {
		return 1, nil
	}
	return float64(limit) / float64(testIntervalS), &limit
}
