package server

import (
	"testing"

	"vista/internal/workerproto"
)

func TestAdmissionKeepsTwentySessionReferenceFeasible(t *testing.T) {
	const eightGiB = 8 << 30
	if err := admitWorker(eightGiB, 19); err != nil {
		t.Fatalf("20th open Session refused on 8 GiB reference: %v", err)
	}
	if err := admitRun(eightGiB, 19); err != nil {
		t.Fatalf("20th execution refused on 8 GiB reference: %v", err)
	}
	if err := admitRun(eightGiB, 20); err == nil {
		t.Fatal("21st concurrent execution was admitted")
	}
	if err := admitWorker(uint64(systemReserve+workerReserve-1), 0); err == nil {
		t.Fatal("Worker admitted below its reservation")
	}
	if err := admitRun(uint64(systemReserve+runReserve-1), 0); err == nil {
		t.Fatal("execution admitted below its reservation")
	}
}
func TestWorkerSoftLimit(t *testing.T) {
	if overWorkerSoftLimit(workerproto.Metrics{HeapBytes: workerHeapSoft, RSSBytes: workerRSSSoft}) {
		t.Fatal("exact soft limit should be permitted")
	}
	if !overWorkerSoftLimit(workerproto.Metrics{HeapBytes: workerHeapSoft + 1}) || !overWorkerSoftLimit(workerproto.Metrics{RSSBytes: workerRSSSoft + 1}) {
		t.Fatal("soft limit crossing was missed")
	}
}
