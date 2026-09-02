package nodes

import "sync"

type StickyNodePool struct {
	pool sync.Map
}

var globalStickyPool = NewStickyNodePool() //nolint:gochecknoglobals

func GetStickyPool() *StickyNodePool {
	return globalStickyPool
}

func NewStickyNodePool() *StickyNodePool {
	return &StickyNodePool{}
}

func (p *StickyNodePool) Add(uri string) {
	p.pool.Store(uri, struct{}{})
}

func (p *StickyNodePool) Evict(uri string) {
	p.pool.Delete(uri)
}

func (p *StickyNodePool) IsSticky(uri string) bool {
	_, exists := p.pool.Load(uri)
	return exists
}

func (p *StickyNodePool) AvailableCount() int {
	n := 0
	p.pool.Range(func(any, any) bool {
		n++
		return true
	})
	return n
}

func (p *StickyNodePool) StaleCount() int {
	return 0
}

func (p *StickyNodePool) List() []string {
	uris := make([]string, 0)
	p.pool.Range(func(k, _ any) bool {
		uris = append(uris, k.(string))
		return true
	})
	return uris
}
