package cache

import (
	"container/list"
	"sync"
)

// entry holds the key and value for an LRU node.
type entry struct {
	key   string
	value interface{}
}

// MemoryLRU is a thread-safe, bounded, in-memory cache using LRU eviction.
type MemoryLRU struct {
	capacity int
	ll       *list.List
	cache    map[string]*list.Element
	mu       sync.RWMutex
}

// NewMemoryLRU creates a new MemoryLRU cache of the given capacity.
func NewMemoryLRU(capacity int) *MemoryLRU {
	if capacity <= 0 {
		capacity = 1000 // safe default
	}
	return &MemoryLRU{
		capacity: capacity,
		ll:       list.New(),
		cache:    make(map[string]*list.Element),
	}
}

// Get retrieves a value from the cache and moves it to the front.
func (c *MemoryLRU) Get(key string) (interface{}, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if ele, hit := c.cache[key]; hit {
		c.ll.MoveToFront(ele)
		return ele.Value.(*entry).value, true
	}
	return nil, false
}

// Set stores a value, optionally evicting the oldest element if at capacity.
func (c *MemoryLRU) Set(key string, value interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if ele, hit := c.cache[key]; hit {
		c.ll.MoveToFront(ele)
		ele.Value.(*entry).value = value
		return
	}

	ele := c.ll.PushFront(&entry{key, value})
	c.cache[key] = ele

	if c.capacity != 0 && c.ll.Len() > c.capacity {
		c.removeOldest()
	}
}

// Delete explicitly removes an element.
func (c *MemoryLRU) Delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if ele, hit := c.cache[key]; hit {
		c.removeElement(ele)
	}
}

// Clear completely wipes the cache.
func (c *MemoryLRU) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.ll = list.New()
	c.cache = make(map[string]*list.Element)
}

// removeOldest removes the least recently used element (internal usage only, must hold lock).
func (c *MemoryLRU) removeOldest() {
	ele := c.ll.Back()
	if ele != nil {
		c.removeElement(ele)
	}
}

// removeElement removes the given list element (internal usage only, must hold lock).
func (c *MemoryLRU) removeElement(e *list.Element) {
	c.ll.Remove(e)
	kv := e.Value.(*entry)
	delete(c.cache, kv.key)
}
