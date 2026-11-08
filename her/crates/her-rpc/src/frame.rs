//! Newline-delimited framing with a size limit. A line is reported as soon as it passes the limit,
//! and the rest of it is dropped as it arrives: however long it goes on, none of it is kept.

use bytes::Bytes;
use bytes::BytesMut;
use std::io;
use tokio::io::AsyncRead;
use tokio::io::AsyncReadExt;

/// Bytes asked of the reader at first. Reads that fill the buffer double the next one, up to
/// `READ_MAX`; short reads halve it again, so an idle connection keeps a small buffer.
const READ_MIN: usize = 4 * 1024;
const READ_MAX: usize = 64 * 1024;
/// Up to this size a line's buffer grows by `realloc`, which is the cheapest way there. Past it the
/// line moves to a new block once, and never by `realloc`: macOS keeps the dirty blocks that large
/// `realloc`s leave behind, 292 MiB after 40 lines of 7 MiB and over 3 GiB with longer lines
/// (protocol-design §4.8).
const LONG_LINE: usize = 4 << 20;
/// That block has room for a line at the default limit (`server.max_frame_bytes`) and one more
/// read, so that such a line does not move again. Its pages are only touched as data arrives.
const LONG_LINE_ROOM: usize = (128 << 20) + READ_MAX;
/// What is kept of an oversized line: enough for the fields that clients write before `params`.
const HEAD: usize = 1024;

#[derive(Debug, PartialEq, Eq)]
pub enum Frame {
    /// One line without its terminator (`\n` or `\r\n`). Never empty or blank.
    Line(Bytes),
    /// A line passed the limit. Reported once, as soon as it does; the rest of it is skipped.
    Oversize {
        /// The start of the line, to tell what it was (see [`peek_request_id`]).
        ///
        /// [`peek_request_id`]: crate::peek_request_id
        head: Bytes,
    },
}

pub struct FrameReader<R> {
    inner: R,
    buf: BytesMut,
    max: usize,
    /// Bytes already searched for a newline.
    scanned: usize,
    /// Inside an oversized line, already reported; drop bytes until the next newline.
    discarding: bool,
    read_size: usize,
}

impl<R: AsyncRead + Unpin> FrameReader<R> {
    pub fn new(inner: R, max_frame_bytes: usize) -> Self {
        Self {
            inner,
            buf: BytesMut::new(),
            max: max_frame_bytes,
            scanned: 0,
            discarding: false,
            read_size: READ_MIN,
        }
    }

    /// The next frame, or `None` at end of input.
    pub async fn next(&mut self) -> io::Result<Option<Frame>> {
        loop {
            if let Some(pos) = memchr::memchr(b'\n', &self.buf[self.scanned..]) {
                let end = self.scanned + pos;
                let mut line = self.buf.split_to(end + 1);
                self.scanned = 0;
                if self.discarding {
                    self.discarding = false;
                    continue;
                }
                line.truncate(end);
                if line.last() == Some(&b'\r') {
                    line.truncate(end - 1);
                }
                if line.len() > self.max {
                    return Ok(Some(oversize(&line)));
                }
                if line.iter().all(u8::is_ascii_whitespace) {
                    continue;
                }
                return Ok(Some(Frame::Line(line.freeze())));
            }
            if self.discarding {
                self.buf.clear();
            } else if self.buf.len() > self.max {
                let frame = oversize(&self.buf);
                self.buf = BytesMut::new();
                self.scanned = 0;
                self.discarding = true;
                return Ok(Some(frame));
            }
            self.scanned = self.buf.len();
            // Give back a buffer that grew for a burst once it has been consumed.
            if self.buf.is_empty() && self.buf.capacity() > 2 * self.read_size {
                self.buf = BytesMut::new();
            }
            self.make_room();
            let spare = self.buf.capacity() - self.buf.len();
            let read = self.inner.read_buf(&mut self.buf).await?;
            self.read_size = if read == spare {
                (self.read_size * 2).min(READ_MAX)
            } else {
                (self.read_size / 2).max(READ_MIN)
            };
            if read == 0 {
                if self.buf.iter().all(u8::is_ascii_whitespace) || self.discarding {
                    return Ok(None);
                }
                // A last line without a terminator.
                let line = self.buf.split();
                self.scanned = 0;
                return Ok(Some(Frame::Line(line.freeze())));
            }
        }
    }

    /// Makes room for the next read.
    fn make_room(&mut self) {
        if self.buf.capacity() > LONG_LINE && self.buf.len() < LONG_LINE {
            self.start_over();
        }
        // `reserve` doubles, and that must not take it past `LONG_LINE`.
        if self.buf.capacity() <= LONG_LINE / 2 {
            self.buf.reserve(self.read_size);
        } else if !self.buf.try_reclaim(self.read_size) {
            let room = self.buf.capacity().saturating_mul(2).max(LONG_LINE_ROOM);
            let room = room.min(self.max.saturating_add(READ_MAX)).max(self.buf.len() + self.read_size);
            let mut moved = BytesMut::with_capacity(room);
            moved.extend_from_slice(&self.buf);
            self.buf = moved;
        }
    }

    /// What follows a long line goes back to the start of its block. Reading on from where it is
    /// would touch all of the block, line after line, however short they are. If the line is still
    /// in use, its block goes with it, and what follows gets a buffer of its own.
    fn start_over(&mut self) {
        let block = LONG_LINE_ROOM.min(self.max.saturating_add(READ_MAX));
        if !self.buf.try_reclaim(block.saturating_sub(self.buf.len())) {
            self.buf = BytesMut::from(&self.buf[..]);
        }
    }
}

fn oversize(line: &[u8]) -> Frame {
    Frame::Oversize {
        head: Bytes::copy_from_slice(&line[..line.len().min(HEAD)]),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    async fn frames(input: &[u8], max: usize) -> Vec<Frame> {
        let mut reader = FrameReader::new(input, max);
        let mut out = Vec::new();
        while let Some(frame) = reader.next().await.unwrap() {
            out.push(frame);
        }
        out
    }

    fn line(s: &str) -> Frame {
        Frame::Line(Bytes::copy_from_slice(s.as_bytes()))
    }

    fn over(head: &[u8]) -> Frame {
        Frame::Oversize {
            head: Bytes::copy_from_slice(head),
        }
    }

    #[tokio::test]
    async fn splits_lines_and_skips_blank_ones() {
        let got = frames(b"{\"a\":1}\n\n  \r\n{\"b\":2}\r\n{\"c\":3}", 1024).await;
        assert_eq!(got, vec![line("{\"a\":1}"), line("{\"b\":2}"), line("{\"c\":3}")]);
    }

    #[tokio::test]
    async fn skips_oversized_lines_and_keeps_going() {
        let mut input = vec![b'x'; 200_000];
        input.extend_from_slice(b"\n{\"ok\":true}\n");
        let got = frames(&input, 1000).await;
        assert_eq!(got, vec![over(&[b'x'; HEAD]), line("{\"ok\":true}")]);
        let got = frames(b"0123456789abc\nshort\n", 10).await;
        assert_eq!(got, vec![over(b"0123456789abc"), line("short")]);
    }

    #[tokio::test]
    async fn the_limit_applies_to_the_line_not_to_its_terminator() {
        for terminator in [&b"\n"[..], b"\r\n"] {
            let mut input = vec![b'x'; 5000];
            input.extend_from_slice(terminator);
            input.extend_from_slice(&[b'y'; 5001]);
            input.extend_from_slice(terminator);
            input.extend_from_slice(b"short\n");
            let got = frames(&input, 5000).await;
            assert_eq!(got.len(), 3, "{terminator:?}");
            assert_eq!(got[0], Frame::Line(Bytes::from(vec![b'x'; 5000])));
            assert_eq!(got[1], over(&[b'y'; HEAD]));
            assert_eq!(got[2], line("short"));
        }
    }

    #[tokio::test]
    async fn a_line_is_reported_when_it_passes_the_limit_not_when_it_ends() {
        let (mut client, server) = tokio::io::duplex(1 << 16);
        use tokio::io::AsyncWriteExt;
        let mut reader = FrameReader::new(server, 1000);
        client.write_all(&[b'x'; 1001]).await.unwrap();
        // No newline has come, and none may ever come.
        assert_eq!(reader.next().await.unwrap(), Some(over(&[b'x'; 1001])));
        assert!(reader.buf.capacity() <= 2 * READ_MAX, "what was read is released: {}", reader.buf.capacity());
        // The rest of the line says nothing more, however long it is.
        client.write_all(&[b'x'; 5000]).await.unwrap();
        client.write_all(b"xxx\n{\"ok\":true}\n").await.unwrap();
        assert_eq!(reader.next().await.unwrap(), Some(line("{\"ok\":true}")));
        // Nor is the line reported again if the input ends inside it.
        client.write_all(&[b'y'; 1001]).await.unwrap();
        assert_eq!(reader.next().await.unwrap(), Some(over(&[b'y'; 1001])));
        client.write_all(b"yyy").await.unwrap();
        drop(client);
        assert_eq!(reader.next().await.unwrap(), None);
    }

    #[tokio::test]
    async fn a_long_line_is_read_whole_however_it_arrives() {
        // Past the point where the buffer moves.
        let long: Vec<u8> = (0..3 * LONG_LINE).map(|i| b'a' + (i % 26) as u8).collect();
        let mut input = b"{\"before\":1}\n".to_vec();
        input.extend_from_slice(&long);
        input.extend_from_slice(b"\n{\"after\":2}\n");
        let (client, server) = tokio::io::duplex(1 << 16);
        let writer = tokio::spawn(async move {
            use tokio::io::AsyncWriteExt;
            let mut client = client;
            client.write_all(&input).await.unwrap();
        });
        let mut reader = FrameReader::new(server, 4 * LONG_LINE);
        assert_eq!(reader.next().await.unwrap(), Some(line("{\"before\":1}")));
        assert_eq!(reader.next().await.unwrap(), Some(Frame::Line(Bytes::from(long))));
        assert_eq!(reader.next().await.unwrap(), Some(line("{\"after\":2}")));
        writer.await.unwrap();
        assert_eq!(reader.next().await.unwrap(), None);
    }

    #[tokio::test]
    async fn what_follows_a_long_line_starts_over() {
        let limit = 128 << 20;
        let (mut client, server) = tokio::io::duplex(1 << 20);
        use tokio::io::AsyncWriteExt;
        let mut reader = FrameReader::new(server, limit);
        let writer = tokio::spawn(async move {
            client.write_all(&vec![b'x'; 2 * LONG_LINE]).await.unwrap();
            client.write_all(b"\n{\"next\":").await.unwrap();
            client
        });
        let frame = reader.next().await.unwrap();
        assert!(matches!(&frame, Some(Frame::Line(line)) if line.len() == 2 * LONG_LINE));
        let mut client = writer.await.unwrap();
        while reader.buf.is_empty() {
            let read = reader.inner.read_buf(&mut reader.buf).await.unwrap();
            assert_ne!(read, 0);
        }
        let start = reader.buf.as_ptr();
        // The line is gone: what follows goes back to the start of the same block.
        drop(frame);
        reader.make_room();
        assert_eq!(reader.buf.capacity(), LONG_LINE_ROOM, "the whole block is ahead");
        assert!(reader.buf.as_ptr() < start);
        client.write_all(b"1}\n").await.unwrap();
        assert_eq!(reader.next().await.unwrap(), Some(line("{\"next\":1}")));

        // The line is still in use: its block goes with it.
        let long = vec![b'y'; 2 * LONG_LINE];
        let writer = tokio::spawn(async move {
            client.write_all(&long).await.unwrap();
            client.write_all(b"\n{\"more\":").await.unwrap();
            client
        });
        let frame = reader.next().await.unwrap();
        assert!(matches!(&frame, Some(Frame::Line(line)) if line.len() == 2 * LONG_LINE));
        let mut client = writer.await.unwrap();
        while reader.buf.is_empty() {
            let read = reader.inner.read_buf(&mut reader.buf).await.unwrap();
            assert_ne!(read, 0);
        }
        reader.make_room();
        assert!(reader.buf.capacity() <= LONG_LINE, "capacity {}", reader.buf.capacity());
        drop(frame);
        client.write_all(b"2}\n").await.unwrap();
        assert_eq!(reader.next().await.unwrap(), Some(line("{\"more\":2}")));
    }

    #[tokio::test]
    async fn room_stops_at_the_limit() {
        let (mut client, server) = tokio::io::duplex(1 << 20);
        use tokio::io::AsyncWriteExt;
        let limit = 3 * LONG_LINE;
        let mut reader = FrameReader::new(server, limit);
        let writer = tokio::spawn(async move {
            client.write_all(&vec![b'x'; limit]).await.unwrap();
            client.write_all(b"\n").await.unwrap();
            client
        });
        // The last read before the line ends: nothing is taken out of the buffer yet.
        while reader.buf.len() <= limit - READ_MIN {
            reader.make_room();
            let read = reader.inner.read_buf(&mut reader.buf).await.unwrap();
            assert_ne!(read, 0);
        }
        reader.make_room();
        assert!(reader.buf.capacity() <= limit + READ_MAX, "room {} for a limit of {limit}", reader.buf.capacity());
        let frame = reader.next().await.unwrap();
        assert!(matches!(&frame, Some(Frame::Line(line)) if line.len() == limit));
        drop(writer.await.unwrap());
    }

    #[tokio::test]
    async fn the_buffer_grows_for_bursts_and_shrinks_when_idle() {
        let (client, server) = tokio::io::duplex(1 << 20);
        let mut reader = FrameReader::new(server, 1 << 20);
        let mut client = client;
        use tokio::io::AsyncWriteExt;
        let big = format!("{}\n", "x".repeat(300_000));
        client.write_all(big.as_bytes()).await.unwrap();
        assert!(matches!(reader.next().await.unwrap(), Some(Frame::Line(line)) if line.len() == 300_000));
        assert!(reader.buf.capacity() >= 300_000 || reader.buf.is_empty());
        for _ in 0..4 {
            client.write_all(b"{\"small\":1}\n").await.unwrap();
            assert_eq!(reader.next().await.unwrap(), Some(line("{\"small\":1}")));
        }
        assert!(reader.buf.capacity() <= 2 * READ_MIN, "capacity {}", reader.buf.capacity());
    }

    #[tokio::test]
    async fn reassembles_lines_split_across_reads() {
        let (client, server) = tokio::io::duplex(8);
        let writer = tokio::spawn(async move {
            use tokio::io::AsyncWriteExt;
            let mut client = client;
            for chunk in [&b"{\"par"[..], b"t\":1}\n{\"next", b"\":2}\n"] {
                client.write_all(chunk).await.unwrap();
                tokio::task::yield_now().await;
            }
        });
        let mut reader = FrameReader::new(server, 1024);
        assert_eq!(reader.next().await.unwrap(), Some(line("{\"part\":1}")));
        assert_eq!(reader.next().await.unwrap(), Some(line("{\"next\":2}")));
        writer.await.unwrap();
        assert_eq!(reader.next().await.unwrap(), None);
    }
}
