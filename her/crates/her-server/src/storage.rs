//! `storage/usage`: where her keeps her data, and how much of each kind there is, so the owner
//! knows what the only copy holds (docs/her-server.md §7). Counted when asked: the size of every
//! file, by the folder it is in; links are not followed.

use std::collections::HashMap;
use std::path::Path;
use std::path::PathBuf;

use her_core::HerHome;
use her_protocol::api::StorageKind;
use her_protocol::api::StoragePart;
use her_protocol::api::StorageUsage;
use her_rpc::RpcError;

use crate::server::Server;

/// Every kind, in the order the result lists them: the enum's, so a kind indexes its bytes.
const KINDS: [StorageKind; 8] = [
    StorageKind::Files,
    StorageKind::Sent,
    StorageKind::Conversations,
    StorageKind::Trash,
    StorageKind::Memory,
    StorageKind::Browser,
    StorageKind::Cache,
    StorageKind::Other,
];

impl Server {
    pub(crate) async fn storage_usage(&self) -> Result<StorageUsage, RpcError> {
        let home = self.0.home.clone();
        tokio::task::spawn_blocking(move || usage(&home)).await.map_err(|e| RpcError::internal(e.to_string()))
    }
}

/// The folders of each kind; a folder in one is that kind too, unless it is listed itself. What
/// no folder claims is `Other`.
fn folders(home: &HerHome) -> HashMap<PathBuf, StorageKind> {
    let root = home.root();
    let mut kinds = HashMap::from([
        (home.chats_dir(), StorageKind::Files),
        (home.sessions_dir(), StorageKind::Conversations),
        // Where deleted conversations go (Server::trash_log, `her sessions remove`).
        (root.join("trash"), StorageKind::Trash),
        (home.browser_dir(), StorageKind::Browser),
        (home.site_icons_dir(), StorageKind::Cache),
        (home.models_dir(), StorageKind::Cache),
        (home.mcp_cache_dir(), StorageKind::Cache),
        (home.mcp_icons_dir(), StorageKind::Cache),
        (home.logs_dir(), StorageKind::Cache),
    ]);
    for bot in std::fs::read_dir(home.bots_dir()).into_iter().flatten().flatten() {
        let id = bot.file_name().to_string_lossy().into_owned();
        kinds.insert(home.bot_files_dir(&id), StorageKind::Files);
        kinds.insert(home.bot_memory_dir(&id), StorageKind::Memory);
        kinds.insert(home.bot_sent_dir(&id), StorageKind::Sent);
    }
    kinds
}

pub(crate) fn usage(home: &HerHome) -> StorageUsage {
    let mut bytes = [0u64; KINDS.len()];
    add(home.root(), StorageKind::Other, &folders(home), &mut bytes);
    let custom = home.user_home().is_none_or(|user| home.root() != user.join(".her"));
    StorageUsage {
        home: home.root().to_path_buf(),
        custom,
        total: bytes.iter().sum(),
        parts: KINDS.iter().zip(bytes).map(|(&kind, bytes)| StoragePart { kind, bytes }).collect(),
    }
}

/// Adds the size of each file under `dir` to its kind. What can't be read counts as nothing.
fn add(dir: &Path, kind: StorageKind, kinds: &HashMap<PathBuf, StorageKind>, bytes: &mut [u64; KINDS.len()]) {
    for entry in std::fs::read_dir(dir).into_iter().flatten().flatten() {
        let Ok(meta) = entry.metadata() else { continue };
        let path = entry.path();
        if meta.is_dir() {
            let kind = kinds.get(&path).copied().unwrap_or(kind);
            add(&path, kind, kinds, bytes);
        } else if meta.is_file() {
            bytes[kind as usize] += meta.len();
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn write(path: PathBuf, size: usize) {
        std::fs::create_dir_all(path.parent().unwrap()).unwrap();
        std::fs::write(path, vec![b'x'; size]).unwrap();
    }

    fn of(u: &StorageUsage, kind: StorageKind) -> u64 {
        u.parts.iter().find(|p| p.kind == kind).unwrap().bytes
    }

    #[test]
    fn each_file_counts_for_the_kind_of_its_folder() {
        let dir = tempfile::tempdir().unwrap();
        let home = HerHome::new(dir.path().join("home"));
        home.ensure().unwrap();
        write(home.bot_files_dir("b1").join("report.md"), 1000);
        write(home.bot_files_dir("b1").join("site/index.html"), 500);
        write(home.chats_dir().join("c1/notes.txt"), 70);
        write(home.bot_memory_dir("b1").join("owner.md"), 30);
        write(home.bot_sent_dir("b1").join("3f9a/report.md"), 800);
        write(home.bot_dir("b1").join("bot.json"), 9);
        write(home.sessions_dir().join("2026/11/15/s1.jsonl"), 400);
        write(home.sessions_dir().join("2026/11/15/s1/tool-output/call_1.txt"), 100);
        write(home.root().join("trash/sessions/s0.jsonl"), 60);
        write(home.browser_dir().join("profile/Cookies"), 200);
        write(home.site_icons_dir().join("a.png"), 5);
        write(home.models_dir().join("lists/mock.json"), 20);
        write(home.mcp_cache_dir().join("sim.json"), 3);
        write(home.mcp_icons_dir().join("sim.png"), 2);
        write(home.logs_dir().join("her.log"), 10);
        write(home.config_file(), 7);
        write(home.skills_dir().join("s/SKILL.md"), 11);
        write(home.root().join("mcp/other.json"), 1);

        let u = usage(&home);
        assert_eq!(u.home, home.root());
        assert!(u.custom, "a home of its own, not the user's ~/.her");
        assert_eq!(u.parts.iter().map(|p| p.kind).collect::<Vec<_>>(), KINDS);
        assert_eq!(of(&u, StorageKind::Files), 1570);
        assert_eq!(of(&u, StorageKind::Conversations), 500);
        assert_eq!(of(&u, StorageKind::Trash), 60);
        assert_eq!(of(&u, StorageKind::Memory), 30);
        assert_eq!(of(&u, StorageKind::Sent), 800, "the copies are a part of their own");
        assert_eq!(of(&u, StorageKind::Browser), 200);
        assert_eq!(of(&u, StorageKind::Cache), 40);
        // The mark her leaves in a home she makes is hers too.
        let mark = std::fs::metadata(home.mark_file()).unwrap().len();
        assert_eq!(of(&u, StorageKind::Other), 28 + mark);
        assert_eq!(u.total, 3228 + mark);
    }

    #[test]
    fn an_empty_home_is_nothing_and_the_default_place_is_not_custom() {
        let dir = tempfile::tempdir().unwrap();
        let home = HerHome::new(dir.path().join(".her")).with_user_home(dir.path());
        let u = usage(&home);
        assert!(!u.custom);
        assert_eq!(u.total, 0);
        assert!(u.parts.iter().all(|p| p.bytes == 0));
    }

    #[cfg(unix)]
    #[test]
    fn links_are_not_followed() {
        let dir = tempfile::tempdir().unwrap();
        let home = HerHome::new(dir.path().join("home"));
        home.ensure().unwrap();
        write(dir.path().join("elsewhere/big.bin"), 5000);
        std::fs::create_dir_all(home.bot_files_dir("b1")).unwrap();
        std::os::unix::fs::symlink(dir.path().join("elsewhere"), home.bot_files_dir("b1").join("linked")).unwrap();
        std::os::unix::fs::symlink(dir.path().join("elsewhere/big.bin"), home.bot_files_dir("b1").join("big.bin")).unwrap();
        // Only her's own mark is counted.
        assert_eq!(usage(&home).total, std::fs::metadata(home.mark_file()).unwrap().len());
    }
}
