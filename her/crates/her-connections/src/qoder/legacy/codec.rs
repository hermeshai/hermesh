//! The `Encode=1` body codec of Qoder's legacy chat: base64 through a shuffled alphabet, with
//! `$` for padding, then the first and last thirds of the text swapped.
//! Protocol reference: crates/her-connections/licenses/README.md.
use base64::{Engine, engine::general_purpose::STANDARD};

const STANDARD_ALPHABET: &[u8; 64] =
    b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
const ALPHABET: &[u8; 64] = b"_doRTgHZBKcGVjlvpC,@aFSx#DPuNJme&i*MzLOEn)sUrthbf%Y^w.(kIQyXqWA!";

/// The text sent for `plain`: 4/3 of its size.
pub(crate) fn encode(plain: &str) -> String {
    let mut text = STANDARD.encode(plain.as_bytes()).into_bytes();
    for b in &mut text {
        *b = match *b {
            b'=' => b'$',
            standard => {
                ALPHABET[STANDARD_ALPHABET
                    .iter()
                    .position(|c| *c == standard)
                    .unwrap_or(0)]
            }
        };
    }
    swap_thirds(&mut text);
    // Every byte is one of the alphabet's or `$`, all ASCII.
    String::from_utf8(text).unwrap_or_default()
}

/// The text `wire` stands for; `None` when it is not this codec's.
#[cfg(test)]
pub(crate) fn decode(wire: &str) -> Option<String> {
    let mut text = wire.as_bytes().to_vec();
    swap_thirds(&mut text);
    for b in &mut text {
        *b = match *b {
            b'$' => b'=',
            coded => STANDARD_ALPHABET[ALPHABET.iter().position(|c| *c == coded)?],
        };
    }
    String::from_utf8(STANDARD.decode(text).ok()?).ok()
}

/// Swaps the first and last thirds (of equal length); the swap undoes itself.
fn swap_thirds(text: &mut [u8]) {
    let third = text.len() / 3;
    if third == 0 {
        return;
    }
    let (head, rest) = text.split_at_mut(third);
    let tail_start = rest.len() - third;
    head.swap_with_slice(&mut rest[tail_start..]);
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn round_trips_and_matches_the_reference_encoder() {
        for plain in ["", "a", "ab", "abc", "{\"k\":\"你好\"}", &"x".repeat(1001)] {
            assert_eq!(decode(&encode(plain)).as_deref(), Some(plain));
            assert_eq!(encode(plain).len(), plain.len().div_ceil(3) * 4);
        }
        // Computed with the reference encoder's algorithm.
        assert_eq!(encode("ab"), "$SB#");
        assert_eq!(encode("abc"), "MSK#");
        assert_eq!(encode("{\"k\":\"你好\"}"), "SWBEw$n*QG(&QPmYKUBM");
        assert!(decode("not the codec ~").is_none());
    }
}
