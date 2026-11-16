//! The service's local endpoint, which only this user's processes reach.
//!
//! On Unix it is a private socket at the endpoint's path. On Windows it is a named pipe that
//! only this user can open: its name is random, and the file at the endpoint's path holds it,
//! as for hermeshd's own endpoints (`internal/localipc`), so hermeshd dials her's as its own.

#[cfg(unix)]
pub use unix::*;
#[cfg(windows)]
pub use windows::*;

#[cfg(unix)]
mod unix {
    use std::io;
    use std::os::fd::AsRawFd;
    use std::os::unix::fs::PermissionsExt;
    use std::path::Path;
    use tokio::net::UnixListener;
    use tokio::net::UnixStream;

    /// A connection the service accepted.
    pub type Stream = UnixStream;
    /// A connection to the service.
    pub type ClientStream = UnixStream;

    pub struct Listener {
        inner: UnixListener,
        uid: libc::uid_t,
    }

    impl Listener {
        /// Listens at `path`. The caller holds the service's lock, so a socket already there is
        /// stale.
        pub fn bind(path: &Path) -> io::Result<Listener> {
            match std::fs::remove_file(path) {
                Ok(()) => {}
                Err(err) if err.kind() == io::ErrorKind::NotFound => {}
                Err(err) => return Err(err),
            }
            let inner = UnixListener::bind(path)?;
            // The longest queue of connections waiting to be accepted that the system allows:
            // 4096 by default on Linux, and on macOS 128 unless `kern.ipc.somaxconn` is raised.
            // Clients retry while it is full (protocol-design §8.2).
            // SAFETY: listening again on the socket just bound only changes the queue length.
            if unsafe { libc::listen(inner.as_raw_fd(), -1) } != 0 {
                tracing::debug!(err = %io::Error::last_os_error(), "kept the default accept queue");
            }
            std::fs::set_permissions(path, std::fs::Permissions::from_mode(0o600))?;
            // SAFETY: geteuid(2) only reads the process's effective user ID.
            let uid = unsafe { libc::geteuid() };
            Ok(Listener { inner, uid })
        }

        /// The next connection and its process, where the system says which; `None` for one
        /// that was refused (another user's) or already gone.
        pub async fn accept(&mut self) -> io::Result<Option<(Stream, Option<u32>)>> {
            let (stream, _) = self.inner.accept().await?;
            match stream.peer_cred() {
                Ok(cred) if cred.uid() == self.uid => {
                    let pid = cred.pid().and_then(|pid| u32::try_from(pid).ok());
                    Ok(Some((stream, pid)))
                }
                Ok(cred) => {
                    tracing::warn!(uid = cred.uid(), "refused a connection from another user");
                    Ok(None)
                }
                // The peer is already gone (a readiness probe, say).
                Err(err) => {
                    tracing::debug!(%err, "dropped a connection whose peer could not be identified");
                    Ok(None)
                }
            }
        }
    }

    /// The two directions of an accepted connection.
    pub fn split(stream: Stream) -> (tokio::net::unix::OwnedReadHalf, tokio::net::unix::OwnedWriteHalf) {
        stream.into_split()
    }

    /// Connects to the service listening at `path`.
    pub async fn connect(path: &Path) -> io::Result<ClientStream> {
        UnixStream::connect(path).await
    }

    /// The two directions of a client's connection: dropping the one that writes ends the
    /// connection for the service.
    pub fn split_client(stream: ClientStream) -> (tokio::net::unix::OwnedReadHalf, tokio::net::unix::OwnedWriteHalf) {
        stream.into_split()
    }

    /// Whether a service takes connections at `path`.
    pub fn answers(path: &Path) -> bool {
        std::os::unix::net::UnixStream::connect(path).is_ok()
    }
}

#[cfg(windows)]
mod windows {
    use std::io;
    use std::os::windows::io::AsRawHandle;
    use std::path::Path;
    use std::time::Duration;
    use tokio::net::windows::named_pipe::ClientOptions;
    use tokio::net::windows::named_pipe::NamedPipeClient;
    use tokio::net::windows::named_pipe::NamedPipeServer;
    use tokio::net::windows::named_pipe::ServerOptions;
    use windows_sys::Win32::Foundation::CloseHandle;
    use windows_sys::Win32::Foundation::ERROR_PIPE_BUSY;
    use windows_sys::Win32::Foundation::HANDLE;
    use windows_sys::Win32::Foundation::LocalFree;
    use windows_sys::Win32::Security::Authorization::ConvertSidToStringSidW;
    use windows_sys::Win32::Security::Authorization::ConvertStringSecurityDescriptorToSecurityDescriptorW;
    use windows_sys::Win32::Security::Authorization::SDDL_REVISION_1;
    use windows_sys::Win32::Security::GetTokenInformation;
    use windows_sys::Win32::Security::PSECURITY_DESCRIPTOR;
    use windows_sys::Win32::Security::SECURITY_ATTRIBUTES;
    use windows_sys::Win32::Security::TOKEN_QUERY;
    use windows_sys::Win32::Security::TOKEN_USER;
    use windows_sys::Win32::Security::TokenUser;
    use windows_sys::Win32::System::Pipes::GetNamedPipeClientProcessId;
    use windows_sys::Win32::System::Threading::GetCurrentProcess;
    use windows_sys::Win32::System::Threading::OpenProcessToken;

    /// A connection the service accepted.
    pub type Stream = NamedPipeServer;
    /// A connection to the service.
    pub type ClientStream = NamedPipeClient;

    const PIPE_PREFIX: &str = r"\\.\pipe\";

    pub struct Listener {
        name: String,
        security: Security,
        /// The instance the next client connects to: there is always one, so that a client
        /// never finds the name gone.
        next: NamedPipeServer,
    }

    impl Listener {
        /// Serves a new pipe and writes its name into the file at `path`.
        pub fn bind(path: &Path) -> io::Result<Listener> {
            let security = Security::this_user()?;
            let name = format!("{PIPE_PREFIX}her-{}", uuid::Uuid::new_v4().simple());
            let next = security.instance(&name, true)?;
            // Whole or not at all: a client reads the name of a pipe that is served.
            let tmp = path.with_extension("tmp");
            std::fs::write(&tmp, &name)?;
            std::fs::rename(&tmp, path)?;
            Ok(Listener { name, security, next })
        }

        /// The next connection and its process, where the system says which. Other users
        /// cannot open the pipe at all, so none is refused here.
        pub async fn accept(&mut self) -> io::Result<Option<(Stream, Option<u32>)>> {
            // Cancelling here loses nothing: the instance stays in `next`.
            self.next.connect().await?;
            let instance = self.security.instance(&self.name, false)?;
            let stream = std::mem::replace(&mut self.next, instance);
            let mut pid = 0u32;
            // SAFETY: the pipe's handle is open, owned by `stream`; `pid` outlives the call.
            let known = unsafe { GetNamedPipeClientProcessId(stream.as_raw_handle() as HANDLE, &mut pid) } != 0;
            Ok(Some((stream, known.then_some(pid))))
        }
    }

    /// The two directions of an accepted connection.
    pub fn split(stream: Stream) -> (tokio::io::ReadHalf<Stream>, tokio::io::WriteHalf<Stream>) {
        tokio::io::split(stream)
    }

    /// The name of the pipe served at `path`.
    fn pipe_name(path: &Path) -> io::Result<String> {
        let name = std::fs::read_to_string(path)?.trim().to_string();
        if !name.starts_with(PIPE_PREFIX) {
            return Err(io::Error::new(io::ErrorKind::InvalidData, format!("{} names no pipe", path.display())));
        }
        Ok(name)
    }

    /// Connects to the service listening at `path`.
    pub async fn connect(path: &Path) -> io::Result<ClientStream> {
        let name = pipe_name(path)?;
        // Every instance is taken for a moment between a connection and the next instance.
        for _ in 0..100 {
            match ClientOptions::new().open(&name) {
                Err(err) if err.raw_os_error() == Some(ERROR_PIPE_BUSY as i32) => {
                    tokio::time::sleep(Duration::from_millis(20)).await;
                }
                other => return other,
            }
        }
        ClientOptions::new().open(&name)
    }

    /// The two directions of a client's connection: dropping the one that writes ends the
    /// connection for the service. A pipe has no half to close, as a socket has: what reads
    /// then ends too, and the pipe closes with both.
    pub fn split_client(stream: ClientStream) -> (ClientRead, ClientWrite) {
        let (read, write) = tokio::io::split(stream);
        let over = tokio_util::sync::CancellationToken::new();
        (
            ClientRead { inner: read, over: Box::pin(over.clone().cancelled_owned()), ended: false },
            ClientWrite { inner: write, _over: over.drop_guard() },
        )
    }

    pub struct ClientRead {
        inner: tokio::io::ReadHalf<ClientStream>,
        over: std::pin::Pin<Box<tokio_util::sync::WaitForCancellationFutureOwned>>,
        ended: bool,
    }

    pub struct ClientWrite {
        inner: tokio::io::WriteHalf<ClientStream>,
        _over: tokio_util::sync::DropGuard,
    }

    impl tokio::io::AsyncRead for ClientRead {
        fn poll_read(mut self: std::pin::Pin<&mut Self>, cx: &mut std::task::Context<'_>, buf: &mut tokio::io::ReadBuf<'_>) -> std::task::Poll<io::Result<()>> {
            use std::future::Future as _;
            if self.ended || self.over.as_mut().poll(cx).is_ready() {
                self.ended = true;
                return std::task::Poll::Ready(Ok(()));
            }
            std::pin::Pin::new(&mut self.inner).poll_read(cx, buf)
        }
    }

    impl tokio::io::AsyncWrite for ClientWrite {
        fn poll_write(mut self: std::pin::Pin<&mut Self>, cx: &mut std::task::Context<'_>, buf: &[u8]) -> std::task::Poll<io::Result<usize>> {
            std::pin::Pin::new(&mut self.inner).poll_write(cx, buf)
        }

        fn poll_flush(mut self: std::pin::Pin<&mut Self>, cx: &mut std::task::Context<'_>) -> std::task::Poll<io::Result<()>> {
            std::pin::Pin::new(&mut self.inner).poll_flush(cx)
        }

        fn poll_shutdown(mut self: std::pin::Pin<&mut Self>, cx: &mut std::task::Context<'_>) -> std::task::Poll<io::Result<()>> {
            std::pin::Pin::new(&mut self.inner).poll_shutdown(cx)
        }
    }

    /// Whether a service takes connections at `path`.
    pub fn answers(path: &Path) -> bool {
        pipe_name(path).is_ok_and(|name| {
            match std::fs::OpenOptions::new().read(true).write(true).open(name) {
                Ok(_) => true,
                Err(err) => err.raw_os_error() == Some(ERROR_PIPE_BUSY as i32),
            }
        })
    }

    /// A security descriptor that admits this user alone.
    struct Security(PSECURITY_DESCRIPTOR);

    // SAFETY: the descriptor is read-only memory that this value owns.
    unsafe impl Send for Security {}
    // SAFETY: as above.
    unsafe impl Sync for Security {}

    impl Security {
        fn this_user() -> io::Result<Security> {
            let sddl: Vec<u16> = format!("D:P(A;;GA;;;{})", user_sid()?).encode_utf16().chain([0]).collect();
            let mut descriptor: PSECURITY_DESCRIPTOR = std::ptr::null_mut();
            // SAFETY: `sddl` ends with a NUL; the descriptor is written to `descriptor`, which
            // this value frees.
            let ok = unsafe {
                ConvertStringSecurityDescriptorToSecurityDescriptorW(sddl.as_ptr(), SDDL_REVISION_1, &mut descriptor, std::ptr::null_mut())
            };
            if ok == 0 {
                return Err(io::Error::last_os_error());
            }
            Ok(Security(descriptor))
        }

        /// One more instance of the pipe `name`; `first` when there must be none yet, so that
        /// nobody else serves the name. Remote clients are refused.
        fn instance(&self, name: &str, first: bool) -> io::Result<NamedPipeServer> {
            let mut attributes = SECURITY_ATTRIBUTES {
                nLength: size_of::<SECURITY_ATTRIBUTES>() as u32,
                lpSecurityDescriptor: self.0,
                bInheritHandle: 0,
            };
            // SAFETY: `attributes` is a valid SECURITY_ATTRIBUTES for the length of the call,
            // and its descriptor lives as long as `self`.
            unsafe {
                ServerOptions::new()
                    .first_pipe_instance(first)
                    .reject_remote_clients(true)
                    .create_with_security_attributes_raw(name, (&raw mut attributes).cast())
            }
        }
    }

    impl Drop for Security {
        fn drop(&mut self) {
            // SAFETY: the descriptor was allocated by the conversion above, for LocalFree.
            unsafe {
                LocalFree(self.0.cast());
            }
        }
    }

    /// This process's user, as a SID string (`S-1-5-21-…`).
    fn user_sid() -> io::Result<String> {
        let mut token: HANDLE = std::ptr::null_mut();
        // SAFETY: the pseudo-handle of this process; the token is written to `token`.
        if unsafe { OpenProcessToken(GetCurrentProcess(), TOKEN_QUERY, &mut token) } == 0 {
            return Err(io::Error::last_os_error());
        }
        // A TOKEN_USER is a pointer to the SID and then the SID itself: u64s align it.
        let mut buffer = [0u64; 32];
        let mut written = 0u32;
        // SAFETY: the buffer is as large as said, and larger than any TOKEN_USER.
        let ok = unsafe { GetTokenInformation(token, TokenUser, buffer.as_mut_ptr().cast(), size_of_val(&buffer) as u32, &mut written) };
        let err = io::Error::last_os_error();
        // SAFETY: the token opened above.
        unsafe {
            CloseHandle(token);
        }
        if ok == 0 {
            return Err(err);
        }
        // SAFETY: the call filled the buffer with a TOKEN_USER, aligned as one.
        let sid = unsafe { (*buffer.as_ptr().cast::<TOKEN_USER>()).User.Sid };
        let mut text: *mut u16 = std::ptr::null_mut();
        // SAFETY: `sid` points into `buffer`; the string is written to `text`, freed below.
        if unsafe { ConvertSidToStringSidW(sid, &mut text) } == 0 {
            return Err(io::Error::last_os_error());
        }
        // SAFETY: `text` is a NUL-terminated string the call allocated, for LocalFree.
        unsafe {
            let len = (0..).take_while(|&i| *text.add(i) != 0).count();
            let sid = String::from_utf16_lossy(std::slice::from_raw_parts(text, len));
            LocalFree(text.cast());
            Ok(sid)
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use tokio::io::AsyncReadExt;
    use tokio::io::AsyncWriteExt;

    /// A client of this user reaches the service, which learns its process; two are served at
    /// once; and nothing answers once the listener is gone.
    #[tokio::test]
    async fn this_user_connects_and_the_service_knows_the_process() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("her.sock");
        assert!(!answers(&path));
        let mut listener = Listener::bind(&path).unwrap();

        let ask = |word: &'static [u8; 4]| {
            let path = path.clone();
            tokio::spawn(async move {
                let mut stream = connect(&path).await.unwrap();
                stream.write_all(word).await.unwrap();
                let mut answer = [0u8; 4];
                stream.read_exact(&mut answer).await.unwrap();
                answer
            })
        };
        let (first, second) = (ask(b"ping"), ask(b"ping"));
        let mut served = Vec::new();
        for _ in 0..2 {
            let (stream, pid) = listener.accept().await.unwrap().expect("a connection of this user's");
            assert_eq!(pid, Some(std::process::id()));
            served.push(split(stream));
        }
        for (read, write) in &mut served {
            let mut asked = [0u8; 4];
            read.read_exact(&mut asked).await.unwrap();
            assert_eq!(&asked, b"ping");
            write.write_all(b"pong").await.unwrap();
        }
        assert_eq!(&first.await.unwrap(), b"pong");
        assert_eq!(&second.await.unwrap(), b"pong");

        // A client that drops what it writes with is gone for the service, which reads no more
        // from it. Both ends at once: a socket's client reads until the service closes, and a
        // pipe's service until the client's reading has ended.
        let client = connect(&path).await.unwrap();
        let (stream, _) = listener.accept().await.unwrap().unwrap();
        let (mut client_read, client_write) = split_client(client);
        drop(client_write);
        let client_done = tokio::spawn(async move {
            let mut rest = Vec::new();
            let _ = client_read.read_to_end(&mut rest).await;
        });
        let (mut read, write) = split(stream);
        let mut rest = Vec::new();
        // Windows says a pipe's other end is gone with an error, a socket with its end.
        let _ = tokio::time::timeout(std::time::Duration::from_secs(10), read.read_to_end(&mut rest)).await.expect("the service sees the client gone");
        drop((read, write));
        tokio::time::timeout(std::time::Duration::from_secs(10), client_done).await.expect("the client's reading ends").unwrap();

        assert!(answers(&path));
        drop(served);
        drop(listener);
        // Windows closes a pipe once its pending reads are cancelled, a moment later.
        for _ in 0..100 {
            if !answers(&path) {
                break;
            }
            tokio::time::sleep(std::time::Duration::from_millis(20)).await;
        }
        assert!(!answers(&path));
    }
}
