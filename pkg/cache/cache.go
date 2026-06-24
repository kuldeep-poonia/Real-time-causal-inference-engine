package cache

// CacheStore defines the enterprise interface for distributed state persistence.
type CacheStore interface {
	// Get retrieves a value from the cache. Returns nil, false if not found.
	Get(key string) (interface{}, bool)
	
	// Set stores a value in the cache with the given key.
	Set(key string, value interface{})
	
	// Delete removes a value from the cache.
	Delete(key string)
	
	// Clear empties the cache completely.
	Clear()
}
