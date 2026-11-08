//! Raising the open file limit. A test file of its own, since it changes a limit of the whole
//! process.

#[test]
fn the_open_file_limit_is_raised_from_the_usual_256() {
    let mut limit = libc::rlimit {
        rlim_cur: 0,
        rlim_max: 0,
    };
    // SAFETY: plain structs; only the soft limit changes.
    unsafe {
        assert_eq!(libc::getrlimit(libc::RLIMIT_NOFILE, &mut limit), 0);
        if limit.rlim_max < 2048 {
            return; // nothing to raise towards
        }
        limit.rlim_cur = 256;
        assert_eq!(libc::setrlimit(libc::RLIMIT_NOFILE, &limit), 0);
    }
    let (before, after) = her_server::limits::raise_open_file_limit().unwrap();
    assert_eq!(before, 256);
    assert!(after >= limit.rlim_max.min(4096), "raised only to {after}");
    // Raising again keeps the limit as it is.
    assert_eq!(her_server::limits::raise_open_file_limit().unwrap(), (after, after));
    // More files than the old limit allowed are open at once.
    let files: Vec<std::fs::File> = (0..1000).map(|_| std::fs::File::open("/dev/null").unwrap()).collect();
    assert_eq!(files.len(), 1000);
}
