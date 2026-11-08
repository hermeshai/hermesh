//! Proposals on the kernel's deck (presentation-deck-edit §2).

use std::io::Read;

use betteroffice_pptx::{EditCtx, FindRequest, Presentation, TextStyle};
use her_slide::{Deck, ProposalEdit, ProposalRequest, ProposalStripStatus, SlideError};

const FIXTURE: &[u8] = include_bytes!("fixtures/editor.pptx");

fn title_edit(deck: &Deck, text: &str) -> ProposalEdit {
    let found = &deck.find_text("Quarterly review").unwrap()[0].range;
    ProposalEdit::ReplaceText { story_id: found.story_id.clone(), start: found.start, end: found.end, text: text.into(), style: None }
}

fn request(edits: Vec<ProposalEdit>) -> ProposalRequest {
    ProposalRequest { agent_id: "agent".into(), note: Some("retitle".into()), edits }
}

fn count(p: &Presentation, text: &str) -> usize {
    p.find_text(&FindRequest { text: text.into(), within: None, limit: None }).unwrap().unwrap().matches.len()
}

fn part(package: &[u8], name: &str) -> Vec<u8> {
    let mut zip = zip::ZipArchive::new(std::io::Cursor::new(package)).unwrap();
    let mut out = Vec::new();
    zip.by_name(name).unwrap().read_to_end(&mut out).unwrap();
    out
}

#[test]
fn a_pending_proposal_changes_neither_replicas_nor_the_saved_file() {
    let deck = Deck::open(FIXTURE.to_vec()).unwrap();
    let replica = Presentation::open_from_update(&deck.encode_state(), 42, Some(FIXTURE)).unwrap();
    let (proposal, update) = deck.propose(request(vec![title_edit(&deck, "Annual review")])).unwrap();
    assert!(update.is_empty(), "proposals stay beside the Yrs document");
    assert_eq!(deck.proposals().unwrap().len(), 1);
    // Read before accepting: the original text.
    assert!(deck.markdown().unwrap().contains("Quarterly review"));
    replica.apply_update_v1(&deck.encode_diff(&replica.encode_state_vector_v1()).unwrap()).unwrap();
    assert!(replica.proposals().unwrap().is_empty());
    let saved = Deck::open(deck.save().unwrap()).unwrap();
    assert_eq!(saved.find_text("Annual review").unwrap().len(), 0);
    assert_eq!(saved.find_text("Quarterly review").unwrap().len(), 1);
    let strip = deck.proposal_strip(&proposal.id).unwrap();
    assert_eq!(strip[0].status, ProposalStripStatus::Changed);
    assert_eq!(strip[1].status, ProposalStripStatus::Unchanged);
}

#[test]
fn an_accepted_proposal_reaches_a_replica_and_saves_only_its_slide() {
    let deck = Deck::open(FIXTURE.to_vec()).unwrap();
    let replica = Presentation::open_from_update(&deck.encode_state(), 42, Some(FIXTURE)).unwrap();
    assert_ne!(deck.client_id(), replica.client_id());
    let (proposal, _) = deck.propose(request(vec![title_edit(&deck, "Annual review")])).unwrap();
    let accepted = deck.accept_proposal(&proposal.id, false).unwrap();
    assert!(accepted.applied);
    assert!(!accepted.update.is_empty());
    replica.apply_update_v1(&accepted.update).unwrap();
    assert_eq!(count(&replica, "Annual review"), 1);
    assert!(deck.proposals().unwrap().is_empty());
    // The guard lets the accepted slide through; the slide it did not touch is byte for byte.
    let saved = deck.save().unwrap();
    assert_eq!(Deck::open(saved.clone()).unwrap().find_text("Annual review").unwrap().len(), 1);
    assert_eq!(part(&saved, "ppt/slides/slide2.xml"), part(FIXTURE, "ppt/slides/slide2.xml"));
    assert_ne!(part(&saved, "ppt/slides/slide1.xml"), part(FIXTURE, "ppt/slides/slide1.xml"));
}

#[test]
fn a_proposal_whose_target_changed_needs_force() {
    let deck = Deck::open(FIXTURE.to_vec()).unwrap();
    let replica = Presentation::open_from_update(&deck.encode_state(), 42, Some(FIXTURE)).unwrap();
    let (proposal, _) = deck.propose(request(vec![title_edit(&deck, "Annual review")])).unwrap();
    let target = &deck.find_text("Quarterly review").unwrap()[0].range;
    replica.insert_text(&EditCtx::local("replica"), &target.story_id, 0, "Draft ", &TextStyle::default()).unwrap();
    deck.apply_update(&replica.encode_diff_v1(&deck.state_vector()).unwrap()).unwrap();
    let refused = deck.accept_proposal(&proposal.id, false).unwrap();
    assert!(!refused.applied);
    assert!(!refused.stale_targets.is_empty());
    assert!(refused.update.is_empty());
    assert_eq!(deck.proposals().unwrap().len(), 1, "still pending");
    assert!(deck.accept_proposal(&proposal.id, true).unwrap().applied);
}

#[test]
fn a_batch_with_a_bad_step_fails_whole_and_names_the_step() {
    let deck = Deck::open(FIXTURE.to_vec()).unwrap();
    let bad = ProposalEdit::DeleteSlide { slide_id: "slide:9:999".into() };
    match deck.propose(request(vec![title_edit(&deck, "Annual review"), bad])) {
        Err(SlideError::Proposal { step, .. }) => assert_eq!(step, Some(1)),
        other => panic!("{other:?}"),
    }
    assert!(deck.proposals().unwrap().is_empty());
    assert!(deck.markdown().unwrap().contains("Quarterly review"));
}

#[test]
fn rejecting_withdraws_and_unknown_ids_are_named() {
    let deck = Deck::open(FIXTURE.to_vec()).unwrap();
    let (proposal, _) = deck.propose(request(vec![title_edit(&deck, "Annual review")])).unwrap();
    assert!(deck.reject_proposal(&proposal.id));
    assert!(!deck.reject_proposal(&proposal.id));
    assert!(deck.proposals().unwrap().is_empty());
    assert!(matches!(deck.accept_proposal(&proposal.id, false), Err(SlideError::NoProposal(_))));
}

#[test]
fn accepted_notes_save_with_their_new_notes_slide() {
    let deck = Deck::open(FIXTURE.to_vec()).unwrap();
    let slide_id = deck.snapshot().unwrap().slides[0].id.clone();
    let (proposal, _) = deck.propose(request(vec![ProposalEdit::SetSlideNotes { slide_id, text: "Say hello".into() }])).unwrap();
    assert!(deck.accept_proposal(&proposal.id, false).unwrap().applied);
    let saved = Deck::open(deck.save().unwrap()).unwrap();
    assert_eq!(saved.outline().unwrap().slides[0].notes, "Say hello");
}

/// The agent's edit is made at once and taken back by undo, on every replica; an edit merged
/// from a replica is not in the deck's history and survives the undo.
#[test]
fn an_applied_edit_reaches_a_replica_and_undo_takes_back_only_it() {
    let deck = Deck::open(FIXTURE.to_vec()).unwrap();
    let replica = Presentation::open_from_update(&deck.encode_state(), 42, Some(FIXTURE)).unwrap();
    let applied = deck.apply(request(vec![title_edit(&deck, "Annual review")])).unwrap();
    assert!(applied.applied);
    assert_eq!(applied.strip[0].status, ProposalStripStatus::Changed);
    assert!(deck.proposals().unwrap().is_empty());
    replica.apply_update_v1(&applied.update).unwrap();
    assert_eq!(count(&replica, "Annual review"), 1);

    // The person edits another slide in the replica; the deck merges it.
    let sv = deck.state_vector();
    let other = &replica.find_text(&FindRequest { text: "Next steps".into(), within: None, limit: None }).unwrap().unwrap().matches[0].range;
    replica.insert_text(&EditCtx::local("person"), &other.story_id, other.start, "The ", &TextStyle::default()).unwrap();
    deck.apply_update(&replica.encode_diff_v1(&sv).unwrap()).unwrap();

    let undo = deck.undo().unwrap().expect("the agent's edit is in the history");
    replica.apply_update_v1(&undo).unwrap();
    assert_eq!(count(&replica, "Annual review"), 0);
    assert_eq!(count(&replica, "Quarterly review"), 1);
    assert_eq!(count(&replica, "The Next steps"), 1, "the person's edit stays");
    assert!(deck.undo().unwrap().is_none(), "nothing else of the deck's own to take back");
}
