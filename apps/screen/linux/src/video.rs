//! From pictures to H.264 for a viewer that shows each frame as it
//! arrives: made smaller to fit, turned into I420, encoded by OpenH264 in
//! real time with no reordering, a keyframe at the start and when asked.
//! A picture that arrives while one is being encoded replaces the one
//! waiting: never a queue.

use std::sync::{Arc, Condvar, Mutex};
use std::time::{Duration, Instant};

use fast_image_resize as fr;
use openh264::encoder::{
    BitRate, Complexity, Encoder, EncoderConfig, FrameRate, FrameType, IntraFramePeriod,
    RateControlMode, UsageType, VuiConfig,
};
use openh264::formats::YUVSlices;
use openh264::OpenH264API;
use yuv::{
    YuvChromaSubsampling, YuvConversionMode, YuvPlanarImageMut, YuvRange, YuvStandardMatrix,
};

use crate::pipe::{self, Out};

/// The order of a pixel's four bytes in memory.
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum Order {
    /// B, G, R and alpha or nothing: X11, wl_shm's XRGB8888, PipeWire's BGRx.
    Bgra,
    /// R, G, B and alpha or nothing.
    Rgba,
}

/// One picture, its rows packed (width × 4 bytes each).
#[derive(Clone)]
pub struct Frame {
    pub pixels: Vec<u8>,
    pub width: u32,
    pub height: u32,
    pub order: Order,
    /// When it was on screen and when it was captured (µs since 1970; zero: not known).
    pub displayed: u64,
    pub captured: u64,
}

impl Frame {
    /// from_rows packs rows that may be further apart than width × 4.
    pub fn from_rows(
        src: &[u8],
        width: u32,
        height: u32,
        stride: usize,
        order: Order,
        flip: bool,
    ) -> Option<Frame> {
        let row = width as usize * 4;
        if width == 0
            || height == 0
            || stride < row
            || src.len() < stride * (height as usize - 1) + row
        {
            return None;
        }
        let mut pixels = vec![0u8; row * height as usize];
        for y in 0..height as usize {
            let from = if flip { height as usize - 1 - y } else { y } * stride;
            pixels[y * row..(y + 1) * row].copy_from_slice(&src[from..from + row]);
        }
        let now = pipe::now_us();
        Some(Frame {
            pixels,
            width,
            height,
            order,
            displayed: now,
            captured: now,
        })
    }
}

/// The bitrates the helper is told, and the frame rates they allow (the
/// Mac's helper steps down the same way).
pub const MIN_BITRATE: u32 = 400_000;
pub const MAX_BITRATE: u32 = 8_000_000;
/// No Linux way gives more, and the encoder takes its time.
pub const MAX_FPS: u32 = 60;

fn fps_for(bps: u32, most: u32) -> u32 {
    if bps < 800_000 {
        most.min(30)
    } else {
        most
    }
}

struct Slot {
    next: Option<Frame>,
    want_key: bool,
    bitrate: u32,
    bitrate_changed: bool,
    max_edge: u32,
    most_fps: u32,
}

/// Video takes the pictures a source captures and sends them, encoded, to
/// hermeshd.
pub struct Video {
    slot: Mutex<Slot>,
    cond: Condvar,
    out: Arc<Out>,
}

impl Video {
    pub fn start(out: Arc<Out>, max_edge: u32, fps: u32, bitrate: u32) -> Arc<Video> {
        let v = Arc::new(Video {
            slot: Mutex::new(Slot {
                next: None,
                want_key: true,
                bitrate: bitrate.clamp(MIN_BITRATE, MAX_BITRATE),
                bitrate_changed: false,
                max_edge: max_edge.clamp(2, 4096),
                most_fps: fps.clamp(1, MAX_FPS),
            }),
            cond: Condvar::new(),
            out,
        });
        let w = v.clone();
        std::thread::Builder::new()
            .name("encode".into())
            .spawn(move || w.run())
            .unwrap();
        v
    }

    /// push hands over the newest picture; one still waiting is dropped.
    pub fn push(&self, f: Frame) {
        let mut s = self.slot.lock().unwrap();
        s.next = Some(f);
        self.cond.notify_one();
    }

    pub fn keyframe(&self) {
        let mut s = self.slot.lock().unwrap();
        s.want_key = true;
        self.cond.notify_one();
    }

    pub fn set_bitrate(&self, bps: u32) {
        let mut s = self.slot.lock().unwrap();
        s.bitrate = bps.clamp(MIN_BITRATE, MAX_BITRATE);
        s.bitrate_changed = true;
    }

    fn run(&self) {
        let mut enc: Option<H264> = None;
        let mut last: Option<Frame> = None;
        let mut encoded_at = Instant::now() - Duration::from_secs(1);
        loop {
            let (frame, key, bitrate, changed, max_edge, fps) = {
                let mut s = self.slot.lock().unwrap();
                while s.next.is_none() && !(s.want_key && last.is_some()) {
                    s = self.cond.wait(s).unwrap();
                }
                // Not more often than the frame rate allows: a picture that comes sooner waits, and a newer one takes its place.
                let fps = fps_for(s.bitrate, s.most_fps);
                let gap = Duration::from_secs(1) / fps;
                let since = encoded_at.elapsed();
                if since < gap {
                    drop(s);
                    std::thread::sleep(gap - since);
                    s = self.slot.lock().unwrap();
                }
                let frame = match s.next.take() {
                    Some(f) => f,
                    None => {
                        // A keyframe asked for while the screen is still: made from the last picture.
                        let mut f = last.clone().unwrap();
                        f.displayed = 0;
                        f.captured = pipe::now_us();
                        f
                    }
                };
                let key = std::mem::take(&mut s.want_key);
                let changed = std::mem::take(&mut s.bitrate_changed);
                (frame, key, s.bitrate, changed, s.max_edge, fps)
            };
            encoded_at = Instant::now();
            let (w, h) = fit(frame.width, frame.height, max_edge);
            if enc.as_ref().map(|e| (e.width, e.height)) != Some((w, h)) {
                match H264::new(w, h, bitrate, fps) {
                    Ok(e) => {
                        self.out.json(
                            pipe::FORMAT,
                            &pipe::Format {
                                width: w,
                                height: h,
                            },
                        );
                        enc = Some(e);
                    }
                    Err(err) => {
                        self.out.state("encoder", &err, false);
                        last = Some(frame);
                        continue;
                    }
                }
            } else if changed {
                if let Some(e) = enc.as_mut() {
                    e.set_bitrate(bitrate, fps);
                }
            }
            let e = enc.as_mut().unwrap();
            if let Some(au) = e.encode(&frame, key) {
                let mut payload = Vec::with_capacity(au.data.len() + 25);
                payload.push(au.key as u8);
                for t in [frame.displayed, frame.captured, pipe::now_us()] {
                    payload.extend_from_slice(&t.to_be_bytes());
                }
                payload.extend_from_slice(&au.data);
                self.out.send(pipe::VIDEO, &payload);
            }
            last = Some(frame);
        }
    }
}

/// fit is the picture's size: the display's own, made smaller to fit
/// max_edge, both sides even.
pub fn fit(w: u32, h: u32, max_edge: u32) -> (u32, u32) {
    let k = (max_edge as f64 / w.max(h) as f64).min(1.0);
    let w = ((w as f64 * k) as u32 / 2 * 2).max(2);
    let h = ((h as f64 * k) as u32 / 2 * 2).max(2);
    (w, h)
}

struct AccessUnit {
    data: Vec<u8>,
    key: bool,
}

struct H264 {
    enc: Encoder,
    width: u32,
    height: u32,
    yuv: YuvPlanarImageMut<'static, u8>,
    scaled: Vec<u8>,
    resizer: fr::Resizer,
}

impl H264 {
    fn new(width: u32, height: u32, bitrate: u32, fps: u32) -> Result<H264, String> {
        // Camera mode: screen-content mode took four times as long a frame
        // (docs/computer-control-linux.md §9). Frame skipping on: without it
        // the encoder does not keep to the bitrate. Slices of an MTU each,
        // encoded on four threads.
        let cfg = EncoderConfig::new()
            .usage_type(UsageType::CameraVideoRealTime)
            .rate_control_mode(RateControlMode::Bitrate)
            .bitrate(BitRate::from_bps(bitrate))
            .max_frame_rate(FrameRate::from_hz(fps as f32))
            .skip_frames(true)
            .complexity(Complexity::Low)
            .intra_frame_period(IntraFramePeriod::from_num_frames(0))
            .num_threads(4)
            .max_slice_len(1200)
            .vui(VuiConfig::bt709());
        let enc =
            Encoder::with_api_config(OpenH264API::from_source(), cfg).map_err(|e| e.to_string())?;
        Ok(H264 {
            enc,
            width,
            height,
            yuv: YuvPlanarImageMut::alloc(width, height, YuvChromaSubsampling::Yuv420),
            scaled: Vec::new(),
            resizer: fr::Resizer::new(),
        })
    }

    fn set_bitrate(&mut self, bps: u32, _fps: u32) {
        let mut info = openh264_sys2::SBitrateInfo {
            iLayer: openh264_sys2::SPATIAL_LAYER_ALL,
            iBitrate: bps as i32,
        };
        unsafe {
            let api = self.enc.raw_api();
            api.set_option(
                openh264_sys2::ENCODER_OPTION_BITRATE,
                std::ptr::addr_of_mut!(info).cast(),
            );
            let mut max = openh264_sys2::SBitrateInfo {
                iLayer: openh264_sys2::SPATIAL_LAYER_ALL,
                iBitrate: bps as i32 * 3 / 2,
            };
            api.set_option(
                openh264_sys2::ENCODER_OPTION_MAX_BITRATE,
                std::ptr::addr_of_mut!(max).cast(),
            );
        }
    }

    fn encode(&mut self, f: &Frame, key: bool) -> Option<AccessUnit> {
        let (w, h) = (self.width, self.height);
        let src: &[u8] = if (f.width, f.height) == (w, h) {
            &f.pixels
        } else {
            self.scaled.resize((w * h * 4) as usize, 0);
            let from = fr::images::ImageRef::new(f.width, f.height, &f.pixels, fr::PixelType::U8x4)
                .ok()?;
            let mut to =
                fr::images::Image::from_slice_u8(w, h, &mut self.scaled, fr::PixelType::U8x4)
                    .ok()?;
            let opts = fr::ResizeOptions::new()
                .resize_alg(fr::ResizeAlg::Convolution(fr::FilterType::Bilinear));
            self.resizer.resize(&from, &mut to, &opts).ok()?;
            &self.scaled
        };
        let convert = match f.order {
            Order::Bgra => yuv::bgra_to_yuv420,
            Order::Rgba => yuv::rgba_to_yuv420,
        };
        convert(
            &mut self.yuv,
            src,
            w * 4,
            YuvRange::Limited,
            YuvStandardMatrix::Bt709,
            YuvConversionMode::Balanced,
        )
        .ok()?;
        if key {
            self.enc.force_intra_frame();
        }
        let planes = YUVSlices::new(
            (
                self.yuv.y_plane.borrow(),
                self.yuv.u_plane.borrow(),
                self.yuv.v_plane.borrow(),
            ),
            (w as usize, h as usize),
            (
                self.yuv.y_stride as usize,
                self.yuv.u_stride as usize,
                self.yuv.v_stride as usize,
            ),
        );
        let bs = self.enc.encode(&planes).ok()?;
        let kind = bs.frame_type();
        if matches!(kind, FrameType::Skip | FrameType::Invalid) {
            return None; // the encoder kept to the bitrate: this one is not sent
        }
        let data = bs.to_vec();
        if data.is_empty() {
            return None;
        }
        Some(AccessUnit {
            data,
            key: matches!(kind, FrameType::IDR),
        })
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn sizes() {
        assert_eq!(fit(1920, 1080, 1920), (1920, 1080));
        assert_eq!(fit(3840, 2160, 1920), (1920, 1080));
        assert_eq!(fit(1366, 768, 1920), (1366, 768));
        assert_eq!(fit(1365, 767, 1920), (1364, 766));
        assert_eq!(fit(2560, 1600, 1920), (1920, 1200));
    }

    #[test]
    fn rows_packed_and_flipped() {
        let src = [1, 2, 3, 4, 9, 9, 5, 6, 7, 8, 9, 9];
        let f = Frame::from_rows(&src, 1, 2, 6, Order::Bgra, false).unwrap();
        assert_eq!(f.pixels, vec![1, 2, 3, 4, 5, 6, 7, 8]);
        let f = Frame::from_rows(&src, 1, 2, 6, Order::Bgra, true).unwrap();
        assert_eq!(f.pixels, vec![5, 6, 7, 8, 1, 2, 3, 4]);
        assert!(Frame::from_rows(&src[..9], 1, 2, 6, Order::Bgra, false).is_none());
    }

    #[test]
    fn encodes_a_keyframe_then_follows() {
        let mut e = H264::new(320, 240, 1_000_000, 30).unwrap();
        let mut f = Frame {
            pixels: vec![200; 320 * 240 * 4],
            width: 320,
            height: 240,
            order: Order::Bgra,
            displayed: 0,
            captured: 0,
        };
        let first = e.encode(&f, true).unwrap();
        assert!(first.key);
        assert_eq!(&first.data[..4], &[0, 0, 0, 1]);
        // The parameter sets come with it.
        assert!(first
            .data
            .windows(5)
            .any(|w| w[..4] == [0, 0, 0, 1] && w[4] & 0x1f == 7));
        f.pixels[1000] = 0;
        if let Some(next) = e.encode(&f, false) {
            assert!(!next.key);
        }
        // A larger picture, made smaller.
        let mut e = H264::new(320, 180, 1_000_000, 30).unwrap();
        let big = Frame {
            pixels: vec![50; 640 * 360 * 4],
            width: 640,
            height: 360,
            order: Order::Rgba,
            displayed: 0,
            captured: 0,
        };
        assert!(e.encode(&big, true).unwrap().key);
    }
}
