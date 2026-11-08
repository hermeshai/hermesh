//! Gives back to the system the memory that the allocator keeps after sessions close or tasks
//! finish. Without this, a long-running service holds on to its peak.

/// Returns the bytes released, where the platform reports them.
pub fn release_free_memory() -> usize {
    platform::release()
}

#[cfg(target_os = "macos")]
mod platform {
    unsafe extern "C" {
        /// `malloc_zone_pressure_relief(NULL, 0)`: every zone, as much as possible.
        fn malloc_zone_pressure_relief(zone: *mut std::ffi::c_void, goal: usize) -> usize;
    }

    pub fn release() -> usize {
        // SAFETY: a null zone means all zones; the call only frees unused pages.
        unsafe { malloc_zone_pressure_relief(std::ptr::null_mut(), 0) }
    }
}

#[cfg(all(target_os = "linux", target_env = "gnu"))]
mod platform {
    pub fn release() -> usize {
        // SAFETY: trims the heap; no pointers are involved.
        unsafe { libc::malloc_trim(0) };
        0
    }
}

#[cfg(not(any(target_os = "macos", all(target_os = "linux", target_env = "gnu"))))]
mod platform {
    pub fn release() -> usize {
        0
    }
}
