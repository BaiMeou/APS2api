package transport

import (
	"fmt"
	"net"
	"sync"
)

type PortLease struct {
	Port int
}

type PortAllocator struct {
	mu     sync.Mutex
	leased map[int]bool
	cursor int
	base   int
	span   int
}

func NewPortAllocator(base, span int) *PortAllocator {
	return &PortAllocator{
		leased: make(map[int]bool),
		base:   base,
		span:   span,
	}
}

func (a *PortAllocator) Acquire() (*PortLease, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	for i := 0; i < a.span; i++ {
		port := a.base + (a.cursor % a.span)
		a.cursor++

		if a.leased[port] {
			continue
		}

		l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			continue
		}
		l.Close()

		a.leased[port] = true
		return &PortLease{Port: port}, nil
	}

	return nil, fmt.Errorf("no available port in range %d-%d", a.base, a.base+a.span-1)
}

func (a *PortAllocator) Release(lease *PortLease) {
	if lease == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.leased, lease.Port)
}

var DefaultPortAllocator = NewPortAllocator(12080, 2000)
