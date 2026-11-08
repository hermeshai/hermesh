/// The catalog's rows of model makers, in search order: where a model's details come from when
/// the catalog has no row for the channel serving it. Only makers: rows of aggregators describe
/// whatever they call by that name (`auto` is a different model on each of them). `alibaba-cn`
/// comes last, since it also hosts models other makers describe better.
pub const MAKERS: &[&str] = &[
    "deepseek",
    "zhipuai",
    "moonshotai-cn",
    "minimax-cn",
    "xiaomi",
    "tencent-tokenhub",
    "openai",
    "anthropic",
    "google",
    "xai",
    "alibaba-cn",
];

/// A model's name as the index of the makers' models has it: FNV-1a, whatever the case. A chance
/// match only means the rows are looked at for nothing.
pub fn model_name_hash(name: &str) -> u32 {
    name.bytes().fold(0x811c_9dc5, |hash, byte| (hash ^ u32::from(byte.to_ascii_lowercase())).wrapping_mul(0x0100_0193))
}
