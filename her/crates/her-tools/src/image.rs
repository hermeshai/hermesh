//! Images that read sends to the model: the format comes from the signature, the size in
//! pixels from the header, without decoding anything.

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum ImageFormat {
    Png,
    Jpeg,
    Gif,
    Webp,
}

impl ImageFormat {
    /// The format whose signature `bytes` start with.
    pub fn detect(bytes: &[u8]) -> Option<Self> {
        if bytes.starts_with(b"\x89PNG\r\n\x1a\n") {
            Some(Self::Png)
        } else if bytes.starts_with(&[0xFF, 0xD8, 0xFF]) {
            Some(Self::Jpeg)
        } else if bytes.starts_with(b"GIF87a") || bytes.starts_with(b"GIF89a") {
            Some(Self::Gif)
        } else if bytes.len() >= 12 && bytes.starts_with(b"RIFF") && &bytes[8..12] == b"WEBP" {
            Some(Self::Webp)
        } else {
            None
        }
    }

    pub fn mime_type(self) -> &'static str {
        match self {
            Self::Png => "image/png",
            Self::Jpeg => "image/jpeg",
            Self::Gif => "image/gif",
            Self::Webp => "image/webp",
        }
    }

    pub fn name(self) -> &'static str {
        match self {
            Self::Png => "PNG",
            Self::Jpeg => "JPEG",
            Self::Gif => "GIF",
            Self::Webp => "WebP",
        }
    }

    /// Width and height in pixels, from the header of an image in this format. `None` when
    /// the header is cut short or malformed.
    pub fn dimensions(self, bytes: &[u8]) -> Option<(u32, u32)> {
        let (width, height) = match self {
            Self::Png => png_size(bytes),
            Self::Jpeg => jpeg_size(bytes),
            Self::Gif => gif_size(bytes),
            Self::Webp => webp_size(bytes),
        }?;
        (width > 0 && height > 0).then_some((width, height))
    }
}

/// The signature, then the IHDR chunk: length 13, type, width, height.
fn png_size(bytes: &[u8]) -> Option<(u32, u32)> {
    let header = bytes.get(8..24)?;
    if header[..8] != *b"\0\0\0\x0dIHDR" {
        return None;
    }
    Some((be32(&header[8..12]), be32(&header[12..16])))
}

/// The logical screen descriptor after the signature.
fn gif_size(bytes: &[u8]) -> Option<(u32, u32)> {
    let screen = bytes.get(6..10)?;
    Some((le16(&screen[..2]), le16(&screen[2..])))
}

/// The first frame header (SOF0 to SOF15), found by skipping the segments before it.
fn jpeg_size(bytes: &[u8]) -> Option<(u32, u32)> {
    let mut at = 2;
    loop {
        if *bytes.get(at)? != 0xFF {
            return None;
        }
        // Any number of 0xFF fill bytes may come before the marker.
        while *bytes.get(at)? == 0xFF {
            at += 1;
        }
        let marker = bytes[at];
        at += 1;
        match marker {
            // Markers without a segment: TEM and RST0 to RST7.
            0x01 | 0xD0..=0xD7 => continue,
            // Not a marker, a second start of image, or the end of the image or of the headers
            // before any frame header.
            0x00 | 0xD8 | 0xD9 | 0xDA => return None,
            // SOF markers; C4, C8 and CC are DHT, JPG and DAC.
            0xC0..=0xCF if !matches!(marker, 0xC4 | 0xC8 | 0xCC) => {
                // Length, sample precision, height, width.
                let frame = bytes.get(at..at + 7)?;
                return Some((be16(&frame[5..7]), be16(&frame[3..5])));
            }
            _ => {
                let length = be16(bytes.get(at..at + 2)?) as usize;
                if length < 2 {
                    return None;
                }
                at += length;
            }
        }
    }
}

/// The first chunk: VP8 (lossy), VP8L (lossless) or VP8X (extended, with the canvas size).
fn webp_size(bytes: &[u8]) -> Option<(u32, u32)> {
    let data = bytes.get(20..)?;
    match bytes.get(12..16)? {
        b"VP8 " => {
            // Frame tag, start code, then 14-bit width and height.
            let frame = data.get(..10)?;
            if frame[3..6] != [0x9D, 0x01, 0x2A] {
                return None;
            }
            Some((le16(&frame[6..8]) & 0x3FFF, le16(&frame[8..10]) & 0x3FFF))
        }
        b"VP8L" => {
            // Signature, then width - 1 and height - 1 in 14 bits each, alpha, version 0.
            let header = data.get(..5)?;
            let bits = u32::from_le_bytes([header[1], header[2], header[3], header[4]]);
            if header[0] != 0x2F || bits >> 29 != 0 {
                return None;
            }
            Some((1 + (bits & 0x3FFF), 1 + ((bits >> 14) & 0x3FFF)))
        }
        b"VP8X" => {
            // Flags, reserved, then canvas width - 1 and height - 1 in 24 bits each.
            let header = data.get(..10)?;
            Some((1 + le24(&header[4..7]), 1 + le24(&header[7..10])))
        }
        _ => None,
    }
}

fn be16(bytes: &[u8]) -> u32 {
    u32::from(u16::from_be_bytes([bytes[0], bytes[1]]))
}

fn be32(bytes: &[u8]) -> u32 {
    u32::from_be_bytes([bytes[0], bytes[1], bytes[2], bytes[3]])
}

fn le16(bytes: &[u8]) -> u32 {
    u32::from(u16::from_le_bytes([bytes[0], bytes[1]]))
}

fn le24(bytes: &[u8]) -> u32 {
    u32::from_le_bytes([bytes[0], bytes[1], bytes[2], 0])
}

#[cfg(test)]
mod tests {
    use super::*;

    fn png(width: u32, height: u32) -> Vec<u8> {
        let mut bytes = b"\x89PNG\r\n\x1a\n\0\0\0\x0dIHDR".to_vec();
        bytes.extend_from_slice(&width.to_be_bytes());
        bytes.extend_from_slice(&height.to_be_bytes());
        bytes.extend_from_slice(&[8, 6, 0, 0, 0]);
        bytes
    }

    fn segment(marker: u8, body: &[u8]) -> Vec<u8> {
        let mut bytes = vec![0xFF, marker];
        bytes.extend_from_slice(&(body.len() as u16 + 2).to_be_bytes());
        bytes.extend_from_slice(body);
        bytes
    }

    fn jpeg(frame_marker: u8, width: u16, height: u16) -> Vec<u8> {
        let mut bytes = vec![0xFF, 0xD8];
        bytes.extend(segment(0xE0, b"JFIF\0\x01\x01\0\0\x01\0\x01\0\0"));
        // An Exif segment longer than 255 bytes, then fill bytes before the next marker.
        bytes.extend(segment(0xE1, &[0xC0; 300]));
        bytes.extend_from_slice(&[0xFF, 0xFF]);
        bytes.extend(segment(0xDB, &[0; 65]));
        // DHT looks like a frame header but is not one.
        bytes.extend(segment(0xC4, &[0x10; 20]));
        let mut frame = vec![8];
        frame.extend_from_slice(&height.to_be_bytes());
        frame.extend_from_slice(&width.to_be_bytes());
        frame.extend_from_slice(&[3, 1, 0x22, 0, 2, 0x11, 1, 3, 0x11, 1]);
        bytes.extend(segment(frame_marker, &frame));
        bytes.extend(segment(0xDA, &[0; 10]));
        bytes
    }

    fn webp(chunk: &[u8; 4], payload: &[u8]) -> Vec<u8> {
        let mut bytes = b"RIFF".to_vec();
        bytes.extend_from_slice(&(payload.len() as u32 + 12).to_le_bytes());
        bytes.extend_from_slice(b"WEBP");
        bytes.extend_from_slice(chunk);
        bytes.extend_from_slice(&(payload.len() as u32).to_le_bytes());
        bytes.extend_from_slice(payload);
        bytes
    }

    #[test]
    fn png_size_comes_from_ihdr() {
        let image = png(1280, 720);
        assert_eq!(ImageFormat::detect(&image), Some(ImageFormat::Png));
        assert_eq!(ImageFormat::Png.dimensions(&image), Some((1280, 720)));
        assert_eq!(ImageFormat::Png.dimensions(&image[..20]), None);
        let mut other_chunk = image.clone();
        other_chunk[12..16].copy_from_slice(b"CgBI");
        assert_eq!(ImageFormat::Png.dimensions(&other_chunk), None);
        // The whole signature is checked.
        let mut damaged = image;
        damaged[6] = b'\n';
        assert_eq!(ImageFormat::detect(&damaged), None);
    }

    #[test]
    fn gif_size_comes_from_the_logical_screen() {
        for signature in [b"GIF87a", b"GIF89a"] {
            let mut image = signature.to_vec();
            image.extend_from_slice(&[0x2C, 0x01, 0x90, 0x01, 0xF7, 0, 0]);
            assert_eq!(ImageFormat::detect(&image), Some(ImageFormat::Gif));
            assert_eq!(ImageFormat::Gif.dimensions(&image), Some((300, 400)));
        }
        assert_eq!(ImageFormat::detect(b"GIF88a\x01\0\x01\0"), None);
    }

    #[test]
    fn jpeg_size_comes_from_the_first_frame_header() {
        for marker in [0xC0, 0xC2, 0xC1, 0xCF] {
            let image = jpeg(marker, 4032, 3024);
            assert_eq!(ImageFormat::detect(&image), Some(ImageFormat::Jpeg));
            assert_eq!(ImageFormat::Jpeg.dimensions(&image), Some((4032, 3024)), "SOF {marker:#X}");
        }
        // Cut before the frame header; image data before any frame header.
        let image = jpeg(0xC0, 10, 10);
        let frame_at = image.windows(2).position(|w| w == [0xFF, 0xC0]).unwrap();
        assert_eq!(ImageFormat::Jpeg.dimensions(&image[..frame_at + 6]), None);
        let mut no_frame = vec![0xFF, 0xD8];
        no_frame.extend(segment(0xDA, &[0; 10]));
        no_frame.extend(segment(0xC0, &[8, 0, 10, 0, 10, 1, 1, 0x11, 0]));
        assert_eq!(ImageFormat::Jpeg.dimensions(&no_frame), None);
        // A segment length below 2 is malformed; a height of 0 is unknown.
        assert_eq!(ImageFormat::Jpeg.dimensions(&[0xFF, 0xD8, 0xFF, 0xE0, 0, 1, 0, 0]), None);
        assert_eq!(ImageFormat::Jpeg.dimensions(&jpeg(0xC0, 640, 0)), None);
    }

    #[test]
    fn webp_size_comes_from_the_first_chunk() {
        // Lossy: frame tag, start code, 14-bit sizes (the top bits hold the scale).
        let lossy = webp(b"VP8 ", &[0x30, 0x01, 0x00, 0x9D, 0x01, 0x2A, 0x80, 0x47, 0x38, 0x04]);
        assert_eq!(ImageFormat::detect(&lossy), Some(ImageFormat::Webp));
        assert_eq!(ImageFormat::Webp.dimensions(&lossy), Some((1920, 1080)));
        let mut bad_start = lossy.clone();
        bad_start[23] = 0;
        assert_eq!(ImageFormat::Webp.dimensions(&bad_start), None);
        // Lossless: width - 1 and height - 1 in 14 bits each.
        let bits: u32 = (1920 - 1) | ((1080 - 1) << 14);
        let mut payload = vec![0x2F];
        payload.extend_from_slice(&bits.to_le_bytes());
        assert_eq!(ImageFormat::Webp.dimensions(&webp(b"VP8L", &payload)), Some((1920, 1080)));
        payload[0] = 0x2E;
        assert_eq!(ImageFormat::Webp.dimensions(&webp(b"VP8L", &payload)), None);
        // Extended: the canvas, width - 1 and height - 1 in 24 bits each.
        let extended = webp(b"VP8X", &[0x10, 0, 0, 0, 0x3F, 0x1F, 0, 0x87, 0x13, 0]);
        assert_eq!(ImageFormat::Webp.dimensions(&extended), Some((8000, 5000)));
        assert_eq!(ImageFormat::detect(b"RIFF\0\0\0\0WAVEfmt "), None);
    }
}
