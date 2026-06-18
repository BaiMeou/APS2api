package transport

import (
	"testing"
)

func TestPortAllocatorAcquire(t *testing.T) {
	a := NewPortAllocator(20000, 100)
	lease, err := a.Acquire()
	if err != nil {
		t.Fatalf("Acquire() failed: %v", err)
	}
	if lease.Port < 20000 || lease.Port >= 20100 {
		t.Errorf("port %d out of range", lease.Port)
	}
	a.Release(lease)
}

func TestPortAllocatorAcquireAfterRelease(t *testing.T) {
	a := NewPortAllocator(20000, 100)
	l1, err := a.Acquire()
	if err != nil {
		t.Fatalf("first Acquire() failed: %v", err)
	}
	a.Release(l1)

	l2, err := a.Acquire()
	if err != nil {
		t.Fatalf("second Acquire() failed: %v", err)
	}
	a.Release(l2)
}

func TestPortAllocatorReleaseNil(t *testing.T) {
	a := NewPortAllocator(20000, 100)
	a.Release(nil)
}

func TestPortAllocatorExhaustion(t *testing.T) {
	a := NewPortAllocator(20000, 5)
	var leases []*PortLease
	for i := 0; i < 5; i++ {
		l, err := a.Acquire()
		if err != nil {
			t.Fatalf("Acquire() %d failed: %v", i, err)
		}
		leases = append(leases, l)
	}
	_, err := a.Acquire()
	if err == nil {
		t.Error("expected error when ports exhausted")
	}
	for _, l := range leases {
		a.Release(l)
	}
}

func TestPortAllocatorDefaultRange(t *testing.T) {
	if DefaultPortAllocator.base != 12080 {
		t.Errorf("expected base 12080, got %d", DefaultPortAllocator.base)
	}
	if DefaultPortAllocator.span != 2000 {
		t.Errorf("expected span 2000, got %d", DefaultPortAllocator.span)
	}
}
