import HermeshHer
import SwiftUI
import UIKit.UIGestureRecognizerSubclass

// Replying to a message of Her's conversation, or to a part of one (docs/her-reply.md §3.3). Touch and hold is the
// way in: Reply, Copy, Select Text. Select Text opens the message's words on a sheet, where a part is selected as
// anywhere on the iPhone, and Reply leads the edit menu. Swiping a message left replies to all of it, as in a
// messenger; nothing is reached by the swipe alone.

/// How a row's message is replied to: to all of it, or to a part selected on a sheet.
struct HerReplying {
    let reply: () -> Void
    let select: () -> Void
}

extension EnvironmentValues {
    /// Set by the conversation for the messages that can be replied to; nil elsewhere, where a bubble only copies.
    @Entry var herReplying: HerReplying?
}

/// The message whose words a sheet offers to select from: its row's key, whose it is, its words.
struct HerSelecting: Identifiable {
    let id: String
    let own: Bool
    let text: String
}

/// What a message replies to, as its bubble shows it: who said it, and what a tap does.
struct HerQuote {
    let reply: HerReply
    /// The assistant's name.
    let name: String
    let show: () -> Void
}

/// What a message replies to, at the top of the owner's bubble: who said it and the words, three lines at most. A
/// tap brings that message into view.
struct HerQuoteCard: View {
    let quote: HerQuote

    var body: some View {
        let who = quote.reply.own ? String(localized: "You") : quote.name
        Button(action: quote.show) {
            HStack(alignment: .top, spacing: 8) {
                RoundedRectangle(cornerRadius: 1.5).fill(.white.opacity(0.8)).frame(width: 3)
                VStack(alignment: .leading, spacing: 1) {
                    Text(who).font(.footnote.weight(.semibold))
                    Text(herPlainText(quote.reply.text)).font(.subheadline).lineLimit(3).multilineTextAlignment(.leading).opacity(0.86)
                }
            }
            .fixedSize(horizontal: false, vertical: true)
            .padding(.leading, 8).padding(.trailing, 10).padding(.vertical, 6)
            .background(.white.opacity(0.17), in: RoundedRectangle(cornerRadius: 11, style: .continuous))
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .accessibilityLabel(Text(verbatim: "\(who): \(quote.reply.text)"))
        .accessibilityHint(Text("Shows the message"))
        .accessibilityIdentifier("her-quote")
    }
}

extension View {
    /// A message's menu (Reply, Copy, Select Text) and the swipe that replies; where the message takes no reply,
    /// Copy alone. VoiceOver reaches Reply through the menu: an action of its own would make the bubble one
    /// element, and its words are read block by block.
    func herMessageActions(copy text: String) -> some View { modifier(HerMessageActions(text: text)) }
}

private struct HerMessageActions: ViewModifier {
    let text: String
    @Environment(\.herReplying) private var replying

    func body(content: Content) -> some View {
        content
            .contextMenu {
                if let replying { Button("Reply", systemImage: "arrowshape.turn.up.left", action: replying.reply) }
                Button("Copy", systemImage: "doc.on.doc") { UIPasteboard.general.string = text }
                if let replying { Button("Select Text", systemImage: "character.cursor.ibeam", action: replying.select) }
            }
            .modifier(HerSwipeToReply(action: replying?.reply))
    }
}

/// Swiping a message left replies to it. The bubble follows the finger a little, an arrow shows where it was, and
/// once it is far enough to reply on letting go, the arrow fills and a tap is felt. A drag that goes up or down is
/// the conversation's scroll, and is left to it.
private struct HerSwipeToReply: ViewModifier {
    let action: (() -> Void)?
    @State private var pull: CGFloat = 0
    @State private var armed = false
    @Environment(\.accessibilityReduceMotion) private var still

    /// How far the bubble goes, and how far the finger must have gone to reply.
    private let reach: CGFloat = 64
    private let enough: CGFloat = 72

    func body(content: Content) -> some View {
        content
            .offset(x: -pull)
            .overlay(alignment: .trailing) {
                // There only during a swipe: at rest a bubble holds nothing but its words.
                if pull > 0 {
                    Image(systemName: "arrowshape.turn.up.left.fill")
                        .font(.footnote.weight(.semibold))
                        .foregroundStyle(armed ? Color.white : Color.secondary)
                        .frame(width: 30, height: 30)
                        .background(armed ? AnyShapeStyle(Color.accentColor) : AnyShapeStyle(Palette.fill2), in: .circle)
                        .scaleEffect(0.6 + 0.4 * min(pull / reach, 1))
                        .opacity(min(pull / (reach * 0.6), 1))
                        .padding(.trailing, 6)
                        .accessibilityHidden(true)
                        .allowsHitTesting(false)
                }
            }
            .gesture(HerLeftSwipe(enabled: action != nil, moved: moved, ended: ended))
    }

    private func moved(_ across: CGFloat) {
        pull = min(max(across, 0) * 0.62, reach)
        let far = across >= enough
        if far && !armed { Haptic.armed.play() }
        armed = far
    }

    /// Let go, where the swipe replies if it went far enough; or taken away (a call comes in), where it doesn't.
    private func ended(_ letGo: Bool) {
        let reply = armed && letGo
        armed = false
        withAnimation(still ? nil : .snappy(duration: 0.25)) { pull = 0 }
        if reply { action?() }
    }
}

/// The swipe itself, as a pan of UIKit's that is only ever a swipe to the left. A drag gesture of SwiftUI's can't
/// turn a drag down: on a quick flick up or down it began in the same touch as the conversation's scrolling would
/// have, took the touch, and the conversation stayed where it was.
private struct HerLeftSwipe: UIGestureRecognizerRepresentable {
    let enabled: Bool
    /// How far left of where it began the finger is.
    let moved: (CGFloat) -> Void
    /// Whether the finger let go (true) or the swipe was taken away.
    let ended: (Bool) -> Void

    func makeUIGestureRecognizer(context: Context) -> LeftPan { LeftPan() }

    func updateUIGestureRecognizer(_ pan: LeftPan, context: Context) { pan.isEnabled = enabled }

    func handleUIGestureRecognizerAction(_ pan: LeftPan, context: Context) {
        switch pan.state {
        case .began, .changed: moved(-pan.translation(in: nil).x)
        case .ended: ended(true)
        case .cancelled, .failed: ended(false)
        default: break
        }
    }

    /// A pan that fails once the finger has gone any way but left, before a scroll view's own pan begins (at 10
    /// points), so every other drag is the scroll's from its first movement.
    final class LeftPan: UIPanGestureRecognizer {
        private var from: CGPoint?

        override func touchesBegan(_ touches: Set<UITouch>, with event: UIEvent) {
            super.touchesBegan(touches, with: event)
            if from == nil { from = touches.first?.location(in: nil) }
        }

        override func touchesMoved(_ touches: Set<UITouch>, with event: UIEvent) {
            if state == .possible, let from, let at = touches.first?.location(in: nil) {
                let across = from.x - at.x, along = abs(at.y - from.y)
                if max(abs(across), along) >= 5, across < along * 1.5 {
                    state = .failed
                    return
                }
            }
            super.touchesMoved(touches, with: event)
        }

        override func reset() {
            super.reset()
            from = nil
        }

        /// The conversation's scrolling waits for this to fail, which it has by then unless the finger goes left:
        /// a swipe scrolls nothing, and a scroll never starts a swipe.
        override func shouldBeRequiredToFail(by other: UIGestureRecognizer) -> Bool { other.view is UIScrollView && !sideways(other) }

        /// Code or a table wider than the bubble scrolls sideways under the finger: that drag is its own.
        override func shouldRequireFailure(of other: UIGestureRecognizer) -> Bool { sideways(other) }

        private func sideways(_ other: UIGestureRecognizer) -> Bool {
            guard let scroll = other.view as? UIScrollView, other === scroll.panGestureRecognizer else { return false }
            return scroll.contentSize.width > scroll.bounds.width
        }
    }
}

/// A message's words on a sheet, to select a part of and reply to it: the system's own text view, so selecting,
/// the loupe and the edit menu are the iPhone's, with Reply first in that menu.
struct HerSelectText: View {
    let text: String
    /// The words selected, when Reply was chosen.
    let reply: (String) -> Void
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        NavigationStack {
            Words(text: text) { picked in
                reply(picked)
                dismiss()
            }
            .ignoresSafeArea(edges: .bottom)
            .navigationTitle("Select Text")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .confirmationAction) { Button("Done") { dismiss() }.accessibilityIdentifier("her-select-done") }
            }
        }
        .presentationDetents([.medium, .large])
    }

    private struct Words: UIViewRepresentable {
        let text: String
        let reply: (String) -> Void

        func makeUIView(context: Context) -> UITextView {
            let view = UITextView()
            view.isEditable = false
            view.isSelectable = true
            view.alwaysBounceVertical = true
            view.backgroundColor = .clear
            view.font = .preferredFont(forTextStyle: .body)
            view.adjustsFontForContentSizeCategory = true
            view.textContainerInset = UIEdgeInsets(top: 12, left: 16, bottom: 32, right: 16)
            view.delegate = context.coordinator
            view.text = text
            view.accessibilityIdentifier = "her-select-text"
            return view
        }

        func updateUIView(_ view: UITextView, context: Context) {
            if view.text != text { view.text = text }
            context.coordinator.reply = reply
        }

        func makeCoordinator() -> Coordinator { Coordinator(reply: reply) }

        final class Coordinator: NSObject, UITextViewDelegate {
            var reply: (String) -> Void

            init(reply: @escaping (String) -> Void) { self.reply = reply }

            func textView(_ textView: UITextView, editMenuForTextIn range: NSRange, suggestedActions: [UIMenuElement]) -> UIMenu? {
                guard range.length > 0, let picked = Range(range, in: textView.text).map({ String(textView.text[$0]) }) else { return nil }
                let action = UIAction(title: String(localized: "Reply"), image: UIImage(systemName: "arrowshape.turn.up.left")) { [weak self] _ in self?.reply(picked) }
                return UIMenu(children: [action] + suggestedActions)
            }
        }
    }
}
