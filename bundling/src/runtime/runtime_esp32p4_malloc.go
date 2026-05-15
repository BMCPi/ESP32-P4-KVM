//go:build esp32p4

package runtime

import "unsafe"

// __wrap_malloc, __wrap_calloc, __wrap_free, __wrap_realloc are required by the
// linker's --wrap=malloc/calloc/free/realloc flags used in the esp32p4 target.
// They redirect all C heap calls (e.g. from littlefs) to TinyGo's GC allocator.

//export __wrap_malloc
func wrap_malloc(size uintptr) unsafe.Pointer {
	if size == 0 {
		return nil
	}
	return alloc(size, nil)
}

//export __wrap_calloc
func wrap_calloc(nmemb, size uintptr) unsafe.Pointer {
	if nmemb == 0 || size == 0 {
		return nil
	}
	// alloc already zeroes the memory
	return alloc(nmemb*size, nil)
}

//export __wrap_free
func wrap_free(ptr unsafe.Pointer) {
	// TinyGo's GC does not require explicit free; the collector will reclaim
	// the memory at the next cycle. Call through to satisfy the interface.
	free(ptr)
}

//export __wrap_realloc
func wrap_realloc(ptr unsafe.Pointer, size uintptr) unsafe.Pointer {
	if size == 0 {
		free(ptr)
		return nil
	}
	// TinyGo's GC has no realloc primitive; allocate a new block.
	// The old block will be reclaimed by the GC. This is safe as long as
	// callers do not retain references to the old pointer after realloc.
	return alloc(size, nil)
}
