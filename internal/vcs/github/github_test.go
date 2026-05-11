package github

import (
	"testing"

	gh "github.com/google/go-github/v66/github"
)

func TestAnyChecksExistStatusCounted(t *testing.T) {
	count := 3
	state := "pending"
	status := &gh.CombinedStatus{TotalCount: &count, State: &state}
	if !anyChecksExist(status, nil) {
		t.Error("expected true for non-zero combined status total")
	}
}

func TestAnyChecksExistCheckRunsCounted(t *testing.T) {
	name := "ci"
	conclusion := "success"
	completed := "completed"
	cr := &gh.CheckRun{Name: &name, Conclusion: &conclusion, Status: &completed}
	checks := &gh.ListCheckRunsResults{CheckRuns: []*gh.CheckRun{cr}}
	if !anyChecksExist(nil, checks) {
		t.Error("expected true for non-empty check runs")
	}
}

func TestAnyChecksExistEmpty(t *testing.T) {
	if anyChecksExist(nil, nil) {
		t.Error("expected false for both nil")
	}
	zero := 0
	if anyChecksExist(&gh.CombinedStatus{TotalCount: &zero}, &gh.ListCheckRunsResults{}) {
		t.Error("expected false for zero total + empty check runs")
	}
}

func TestCombineStatesFailureWins(t *testing.T) {
	// A failed combined status short-circuits everything else.
	count := 1
	state := "failure"
	st := &gh.CombinedStatus{TotalCount: &count, State: &state}
	got, _ := combineStates(st, nil)
	if got != "failure" {
		t.Errorf("got %q, want failure", got)
	}
}

func TestCombineStatesCheckRunFailureSurfaced(t *testing.T) {
	completed := "completed"
	conclusion := "failure"
	name := "ci"
	cr := &gh.CheckRun{Status: &completed, Conclusion: &conclusion, Name: &name}
	checks := &gh.ListCheckRunsResults{CheckRuns: []*gh.CheckRun{cr}}
	got, msg := combineStates(nil, checks)
	if got != "failure" {
		t.Errorf("got %q, want failure", got)
	}
	if msg == "" {
		t.Error("expected failure message")
	}
}

func TestCombineStatesAllPassSuccess(t *testing.T) {
	count := 1
	state := "success"
	st := &gh.CombinedStatus{TotalCount: &count, State: &state}
	completed := "completed"
	conclusion := "success"
	cr := &gh.CheckRun{Status: &completed, Conclusion: &conclusion}
	checks := &gh.ListCheckRunsResults{CheckRuns: []*gh.CheckRun{cr}}
	got, _ := combineStates(st, checks)
	if got != "success" {
		t.Errorf("got %q, want success", got)
	}
}

func TestCombineStatesPendingWaits(t *testing.T) {
	count := 1
	state := "pending"
	st := &gh.CombinedStatus{TotalCount: &count, State: &state}
	got, _ := combineStates(st, nil)
	if got != "pending" {
		t.Errorf("got %q, want pending", got)
	}
}
