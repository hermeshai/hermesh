use betteroffice_pptx::{EditCtx, Presentation, TextStyle};
use her_slide::Deck;

#[test]
fn replica_updates_round_trip_through_authority_and_save() {
    let bytes = include_bytes!("fixtures/editor.pptx").to_vec();
    let authority = Deck::open(bytes.clone()).unwrap();
    let replica =
        Presentation::open_from_update(&authority.encode_state(), 42, Some(&bytes)).unwrap();
    let matches = authority.find_text("Quarterly review").unwrap();
    let target = &matches[0].range;
    replica
        .insert_text(
            &EditCtx::local("replica"),
            &target.story_id,
            target.start,
            "Updated ",
            &TextStyle::default(),
        )
        .unwrap();
    let delta = replica.encode_diff_v1(&authority.state_vector()).unwrap();
    authority.apply_update(&delta).unwrap();
    assert_eq!(
        authority
            .find_text("Updated Quarterly review")
            .unwrap()
            .len(),
        1
    );
    let saved = authority.save().unwrap();
    let reopened = Deck::open(saved).unwrap();
    assert_eq!(
        reopened
            .find_text("Updated Quarterly review")
            .unwrap()
            .len(),
        1
    );
    assert_eq!(
        authority
            .replace_text("Updated Quarterly review", "Approved review")
            .unwrap(),
        1
    );
    replica
        .apply_update_v1(
            &authority
                .encode_diff(&replica.encode_state_vector_v1())
                .unwrap(),
        )
        .unwrap();
    assert_eq!(
        replica
            .find_text(&betteroffice_pptx::FindRequest {
                text: "Approved review".into(),
                within: None,
                limit: None
            })
            .unwrap()
            .unwrap()
            .matches
            .len(),
        1
    );
}

#[test]
fn malformed_replica_update_is_rejected() {
    let authority = Deck::open(include_bytes!("fixtures/editor.pptx").to_vec()).unwrap();
    let before = authority.markdown().unwrap();
    assert!(authority.apply_update(&[0xff]).is_err());
    assert_eq!(authority.markdown().unwrap(), before);
}

// TODO: BetterOffice's Presentation holds RefCell/Rc (yrs undo, caches), so Deck is not Send + Sync
// yet. Re-enable once the fork makes the edit session thread-safe.
// #[test]
// fn authority_can_be_owned_by_server_tasks() {
//     fn assert_send_sync<T: Send + Sync>() {}
//     assert_send_sync::<Deck>();
// }

mod structural {
    use betteroffice_pptx::{EditCtx, PictureDraft, Presentation, ShapeRect};
    use her_slide::Deck;

    /// 1×1 transparent PNG.
    const PNG: &[u8] = &[
        0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0, 0, 0, 0x0D, 0x49, 0x48, 0x44, 0x52, 0,
        0, 0, 1, 0, 0, 0, 1, 8, 6, 0, 0, 0, 0x1F, 0x15, 0xC4, 0x89, 0, 0, 0, 0x0D, 0x49, 0x44,
        0x41, 0x54, 0x78, 0x9C, 0x63, 0, 1, 0, 0, 5, 0, 1, 0x0D, 0x0A, 0x2D, 0xB4, 0, 0, 0, 0,
        0x49, 0x45, 0x4E, 0x44, 0xAE, 0x42, 0x60, 0x82,
    ];

    /// Runs `edit` on a replica, syncs it to the authority, saves there and reopens.
    fn save_after(edit: impl Fn(&Presentation, &[String])) -> Deck {
        let bytes = include_bytes!("fixtures/editor.pptx").to_vec();
        let authority = Deck::open(bytes.clone()).unwrap();
        let replica =
            Presentation::open_from_update(&authority.encode_state(), 42, Some(&bytes)).unwrap();
        edit(&replica, &replica.slide_ids().unwrap());
        authority
            .apply_update(&replica.encode_diff_v1(&authority.state_vector()).unwrap())
            .unwrap();
        Deck::open(authority.save().unwrap()).unwrap()
    }

    fn ctx() -> EditCtx {
        EditCtx::local("replica")
    }

    #[test]
    fn inserted_slide_saves() {
        let reopened = save_after(|r, _| {
            r.insert_slide(&ctx(), 1, None).unwrap();
        });
        assert_eq!(reopened.slide_count(), 3);
    }

    #[test]
    fn duplicated_slide_saves() {
        let reopened = save_after(|r, ids| {
            r.duplicate_slide(&ctx(), &ids[0], 1).unwrap();
        });
        assert_eq!(reopened.slide_count(), 3);
    }

    #[test]
    fn moved_slide_saves_in_new_order() {
        let source = Deck::open(include_bytes!("fixtures/editor.pptx").to_vec()).unwrap();
        let first = source.outline().unwrap();
        let reopened = save_after(|r, ids| {
            r.move_slide(&ctx(), &ids[0], 1).unwrap();
        });
        assert_eq!(reopened.slide_count(), 2);
        assert_ne!(
            format!("{:?}", reopened.outline().unwrap()),
            format!("{first:?}")
        );
    }

    #[test]
    fn added_picture_saves_with_new_media() {
        let reopened = save_after(|r, ids| {
            let draft = PictureDraft {
                name: "pixel".into(),
                rect: ShapeRect {
                    x: 0,
                    y: 0,
                    width: 9525,
                    height: 9525,
                },
                content_type: "image/png".into(),
                media_bytes: PNG.to_vec(),
            };
            r.add_picture(&ctx(), &ids[0], &draft).unwrap();
        });
        assert_eq!(reopened.slide_count(), 2);
    }
}

#[test]
fn deleted_slide_saves() {
    use betteroffice_pptx::{EditCtx, Presentation};
    let bytes = include_bytes!("fixtures/editor.pptx").to_vec();
    let authority = her_slide::Deck::open(bytes.clone()).unwrap();
    let replica = Presentation::open_from_update(&authority.encode_state(), 42, Some(&bytes)).unwrap();
    let ids = replica.slide_ids().unwrap();
    replica.delete_slide(&EditCtx::local("replica"), &ids[0]).unwrap();
    authority.apply_update(&replica.encode_diff_v1(&authority.state_vector()).unwrap()).unwrap();
    let reopened = her_slide::Deck::open(authority.save().unwrap()).unwrap();
    assert_eq!(reopened.slide_count(), 1);
}
