//! Live-engine integration tests for the transport (ADR-0046, ADR-0047).
//!
//! These are `#[ignore]`d: they need a running engine (default
//! `http://127.0.0.1:9100`, override `ENGINE_URL`/`ENGINE_PORT`). Run them with:
//!
//! ```sh
//! cargo test --test live_engine -- --ignored --nocapture --test-threads=1
//! ```
//!
//! `--test-threads=1` is required: the engine's SQLite writer serializes, so
//! concurrent opens surface `SQLITE_BUSY`.
//!
//! They exercise the exact generated calls `bridge::Worker::approve` uses:
//! open → stage → commit (write-through) and the typed `file-changed-externally`
//! 409. No model is required (the diff/approve path is deterministic HTTP).

use texteditor_tui_rs::discovery::{self, EngineEnv};
use texteditor_tui_rs::gen::{
    BlockEdit, CommitDocumentApiError, FileChangedExternallyError, HttpClient, OpenDocumentRequest,
    OpenRequest, OpenResultKind,
};

fn scratch_path(name: &str) -> std::path::PathBuf {
    let dir = std::env::temp_dir().join(format!("tui-rs-live-{}", std::process::id()));
    std::fs::create_dir_all(&dir).expect("create scratch dir");
    dir.join(name)
}

async fn connected() -> (HttpClient, String) {
    let base = discovery::discover(&EngineEnv::from_process_env())
        .await
        .expect("live engine reachable");
    let client = HttpClient::new().with_base_url(base.clone());
    (client, base)
}

#[tokio::test]
#[ignore = "requires a running engine"]
async fn approve_writes_the_accepted_edit_through_to_disk() {
    let (client, base) = connected().await;
    println!("engine: {base}");

    let path = scratch_path("approve.md");
    std::fs::write(&path, "The original sentence.\n").unwrap();

    let document = client
        .open_document(OpenDocumentRequest {
            path: path.to_string_lossy().into_owned(),
        })
        .await
        .expect("open document");
    let blocks = client.get_blocks(&document.id).await.expect("blocks");
    let block = blocks.first().expect("one block");
    let block_id = block.id.clone();

    client
        .apply_edit(
            &document.id,
            BlockEdit::new(block_id.clone(), "The rewritten sentence.".to_string()),
        )
        .await
        .expect("stage edit");

    let revision = client
        .commit_document(&document.id, None)
        .await
        .expect("commit");
    assert_eq!(revision.written_through, Some(true), "must write through");
    assert_eq!(revision.path.as_deref(), Some(document.path.as_str()));

    let on_disk = std::fs::read_to_string(&path).unwrap();
    assert!(
        on_disk.contains("The rewritten sentence."),
        "approved edit not on disk: {on_disk:?}"
    );
    let _ = std::fs::remove_file(&path);
}

#[tokio::test]
#[ignore = "requires a running engine"]
async fn external_change_is_a_labeled_conflict_and_never_clobbers() {
    let (client, _base) = connected().await;

    let path = scratch_path("conflict.md");
    std::fs::write(&path, "The original sentence.\n").unwrap();

    let document = client
        .open_document(OpenDocumentRequest {
            path: path.to_string_lossy().into_owned(),
        })
        .await
        .expect("open document");
    let blocks = client.get_blocks(&document.id).await.expect("blocks");
    let block = blocks.first().expect("one block");

    client
        .apply_edit(
            &document.id,
            BlockEdit::new(block.id.clone(), "An engine rewrite.".to_string()),
        )
        .await
        .expect("stage edit");

    // An external writer (Obsidian, an editor) changes the file after staging.
    std::fs::write(&path, "Changed externally, out of band.\n").unwrap();

    let err = client
        .commit_document(&document.id, None)
        .await
        .expect_err("commit must refuse an external change");
    let typed = err.api().and_then(|api| api.typed.as_ref());
    match typed {
        Some(CommitDocumentApiError::Status409(conflict)) => {
            assert_eq!(
                conflict.error,
                FileChangedExternallyError::FileChangedExternally
            );
            assert_eq!(conflict.path, document.path);
        }
        other => panic!("expected typed 409 conflict, got {other:?} (err: {err})"),
    }

    let on_disk = std::fs::read_to_string(&path).unwrap();
    assert_eq!(on_disk, "Changed externally, out of band.\n", "no clobber");
    let _ = std::fs::remove_file(&path);
}

#[tokio::test]
#[ignore = "requires a running engine"]
async fn open_and_accept_are_one_verb_each() {
    let (client, _base) = connected().await;

    let path = scratch_path("accept.md");
    std::fs::write(&path, "The original sentence.\n").unwrap();

    // One POST /open resolves workspace + document + blocks + session + modes.
    let result = client
        .open(OpenRequest {
            path: path.to_string_lossy().into_owned(),
            anchor_block_id: None,
            mode_type: None,
        })
        .await
        .expect("open");
    assert_eq!(result.kind, OpenResultKind::Document);
    let document = result.document.expect("document");
    let block = result
        .blocks
        .expect("blocks")
        .into_iter()
        .next()
        .expect("one block");
    assert!(result.session.is_some(), "open resumes or creates a session");
    assert!(result.modes.is_some(), "open returns the presets");

    // Stage (as a turn would), then accept atomically — no candidate text sent.
    client
        .apply_edit(
            &document.id,
            BlockEdit::new(block.id.clone(), "The rewritten sentence.".to_string()),
        )
        .await
        .expect("stage edit");
    let revision = client
        .accept_block(&document.id, &block.id, None)
        .await
        .expect("accept");
    assert_eq!(revision.written_through, Some(true), "must write through");
    assert_eq!(revision.path.as_deref(), Some(document.path.as_str()));

    let on_disk = std::fs::read_to_string(&path).unwrap();
    assert!(
        on_disk.contains("The rewritten sentence."),
        "accepted edit not on disk: {on_disk:?}"
    );
    let _ = std::fs::remove_file(&path);
}
