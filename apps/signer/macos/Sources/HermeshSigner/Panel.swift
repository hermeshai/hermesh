import AppKit
import HermeshKit
import LocalAuthentication
import LocalAuthenticationEmbeddedUI
import SwiftUI

/// What the floating panel shows, and the person's answers.
@MainActor
final class PanelModel: ObservableObject {
    enum State {
        case hidden
        /// A batch to approve or reject.
        case confirm(Confirmation)
        /// A batch was rejected because it cannot be shown, or signing failed.
        case notice(title: String, lines: [String])
        /// After adding a device: the digits to type on it.
        case match(Confirmation.JoinMatch)
    }

    @Published var state = State.hidden
    /// The context the embedded Touch ID view is bound to; nil with --test-key.
    @Published var auth: LAContext?
    /// Touch ID can be used; otherwise the login password is asked for.
    @Published var biometry = true
    @Published var authError: String?
    @Published var working = false
    /// The first setup's cards are shown (behind Details until asked for).
    @Published var details = false

    var onReject: () -> Void = {}
    var onRetry: () -> Void = {}
    /// With --test-key and no --test-decision: a button stands in for Touch ID.
    var onTestApprove: (() -> Void)?
    var onDismiss: () -> Void = {}
    var onToggleDetails: () -> Void = {}
}

/// The floating panel. Its hosting view does not size the window: letting
/// SwiftUI do that recurses while AppKit updates constraints. The panel is
/// sized from a separate measurement instead, whenever it is shown. That
/// measurement has no title bar, so the hidden one must not inset the
/// content either: it would push it down and squeeze the scrolling cards.
@MainActor
final class Panel {
    let model = PanelModel()
    private var panel: NSPanel?

    func show() {
        if panel == nil {
            let hv = NSHostingView(rootView: PanelContent().environmentObject(model))
            hv.sizingOptions = []
            hv.safeAreaRegions = []
            let p = NSPanel(contentRect: NSRect(x: 0, y: 0, width: 440, height: 300), styleMask: [.titled, .fullSizeContentView], backing: .buffered, defer: false)
            p.contentView = hv
            p.title = String(localized: "Hermesh Approval", bundle: .module)
            p.titlebarAppearsTransparent = true
            p.titleVisibility = .hidden
            for b in [NSWindow.ButtonType.closeButton, .miniaturizeButton, .zoomButton] { p.standardWindowButton(b)?.isHidden = true }
            p.level = .floating
            p.collectionBehavior = [.canJoinAllSpaces, .fullScreenAuxiliary]
            p.isReleasedWhenClosed = false
            p.hidesOnDeactivate = false
            panel = p
        }
        guard let p = panel else { return }
        let size = Self.measure(model)
        if size.width > 0, size.height > 0, size != p.contentRect(forFrameRect: p.frame).size {
            p.setContentSize(size)
        }
        if p.isVisible { return }
        p.center()
        NSApp.activate(ignoringOtherApps: true)
        p.makeKeyAndOrderFront(nil)
    }

    func hide() {
        model.state = .hidden
        model.auth = nil
        panel?.orderOut(nil)
    }

    /// The size the panel's content wants for model's current state.
    /// Measured on a copy without the Touch ID context: only the panel's
    /// own view may bind an authentication view to it.
    static func measure(_ model: PanelModel) -> NSSize {
        let m = PanelModel()
        m.state = model.state
        m.authError = model.authError
        m.working = model.working
        m.biometry = model.biometry
        m.onTestApprove = model.onTestApprove
        m.details = model.details
        return NSHostingView(rootView: PanelContent().environmentObject(m)).fittingSize
    }
}

struct PanelContent: View {
    @EnvironmentObject var model: PanelModel

    var body: some View {
        switch model.state {
        case .hidden: EmptyView()
        case .confirm(let c): ConfirmView(confirmation: c)
        case .notice(let title, let lines): NoticeView(title: title, lines: lines)
        case .match(let m): MatchView(match: m)
        }
    }
}

/// Shows exactly what the root key is about to sign, with Touch ID
/// embedded at the bottom: one request on its own (HeroCard), or several as
/// a list of cards.
struct ConfirmView: View {
    @EnvironmentObject var model: PanelModel
    let confirmation: Confirmation

    /// The one request of a batch that is not a summary with details.
    private var single: Card? { confirmation.summary == nil && confirmation.cards.count == 1 ? confirmation.cards[0] : nil }

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            if let card = single {
                HeroCard(card: card, caller: confirmation.caller)
            } else {
                HStack(spacing: 10) {
                    Image(systemName: "lock.shield").font(.largeTitle).foregroundStyle(.tint)
                    VStack(alignment: .leading, spacing: 2) {
                        Text("Needs your confirmation", bundle: .module).font(.title3.bold())
                        Text(confirmation.caller.label).font(.callout).foregroundStyle(.secondary)
                    }
                }
                if !confirmation.caller.isHermesh { CallerWarning(caller: confirmation.caller) }
                ScrollView {
                    VStack(alignment: .leading, spacing: 10) {
                        if let summary = confirmation.summary {
                            Text(summary).font(.body).fixedSize(horizontal: false, vertical: true)
                            Button(model.details ? String(localized: "Hide Details", bundle: .module) : String(localized: "Details", bundle: .module)) { model.onToggleDetails() }
                                .buttonStyle(.link)
                                .font(.callout)
                        }
                        if confirmation.summary == nil || model.details {
                            ForEach(Array(confirmation.cards.enumerated()), id: \.offset) { CardView(card: $0.element) }
                        }
                    }
                    .frame(maxWidth: .infinity, alignment: .leading)
                }
                .frame(maxHeight: 380)
            }
            Divider()
            ApproveBar()
        }
        .padding(20)
        .frame(width: 440)
    }
}

/// Touch ID (or the password) to approve, and Deny.
struct ApproveBar: View {
    @EnvironmentObject var model: PanelModel

    var body: some View {
        HStack(spacing: 12) {
            if let ctx = model.auth, model.biometry {
                TouchIDView(context: ctx).id(ObjectIdentifier(ctx)).frame(width: TouchIDView.side, height: TouchIDView.side)
            } else {
                Image(systemName: model.biometry ? "touchid" : "lock").font(.title).foregroundStyle(.secondary).frame(width: TouchIDView.side, height: TouchIDView.side)
            }
            VStack(alignment: .leading, spacing: 2) {
                Text(model.working ? String(localized: "Signing…", bundle: .module)
                     : model.biometry ? String(localized: "Approve with Touch ID", bundle: .module)
                     : String(localized: "Enter your login password to approve", bundle: .module)).font(.callout)
                if let e = model.authError {
                    Text(e).font(.caption).foregroundStyle(.red)
                    Button(String(localized: "Try Again", bundle: .module)) { model.onRetry() }.controlSize(.small)
                }
            }
            Spacer()
            if let approve = model.onTestApprove {
                Button(String(localized: "Approve (Test)", bundle: .module), action: approve).disabled(model.working)
            }
            Button(String(localized: "Deny", bundle: .module)) { model.onReject() }
                .keyboardShortcut(.cancelAction)
                .disabled(model.working)
        }
    }
}

/// Not started by Hermesh: who did, and to reject what one did not start.
struct CallerWarning: View {
    let caller: CallerInfo

    var body: some View {
        Label {
            VStack(alignment: .leading, spacing: 2) {
                Text(CallerInfo.warning)
                if !caller.path.isEmpty {
                    Text(String(localized: "Location: \(caller.path)", bundle: .module, comment: "Where the program that made the request is")).foregroundStyle(.secondary).textSelection(.enabled)
                }
            }
        } icon: {
            Image(systemName: "exclamationmark.triangle.fill").foregroundStyle(.orange)
        }
        .font(.callout)
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(8)
        .background(.orange.opacity(0.15), in: RoundedRectangle(cornerRadius: 8))
    }
}

/// One request on its own: what it is, centred under the shield; the code
/// to compare; its facts as a grouped list; the warning. Details behind ⓘ
/// in the corner.
struct HeroCard: View {
    let card: Card
    let caller: CallerInfo

    private var details: [String] { (card.rows.isEmpty ? [] : card.lines) + card.ids }

    var body: some View {
        VStack(spacing: 16) {
            VStack(spacing: 6) {
                Image(systemName: "lock.shield.fill").font(.system(size: 30)).foregroundStyle(.tint).padding(.bottom, 2)
                Text(caller.label).font(.caption).foregroundStyle(.secondary)
                Text(card.title).font(.title3.weight(.semibold)).multilineTextAlignment(.center)
                Text(card.why).font(.callout).foregroundStyle(.secondary).multilineTextAlignment(.center).fixedSize(horizontal: false, vertical: true)
            }
            .frame(maxWidth: .infinity)
            if !caller.isHermesh { CallerWarning(caller: caller) }
            if let code = card.code { CodeChips(code: code) }
            FactsList(card: card)
            if let w = card.warning {
                Label(w, systemImage: "exclamationmark.triangle.fill")
                    .font(.callout)
                    .foregroundStyle(.orange)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
        }
        .overlay(alignment: .topTrailing) {
            if !details.isEmpty { IDsButton(ids: details) }
        }
    }
}

/// A card's facts as a grouped list: its table (an icon per kind of fact) or
/// its lines.
struct FactsList: View {
    let card: Card

    private struct Fact { let symbol: String; let label: String?; let value: String }

    private static func symbol(_ kind: CardRow.Kind) -> String {
        switch kind {
        case .project: return "folder"
        case .machine: return "desktopcomputer"
        case .codingAgent: return "terminal"
        case .expires: return "calendar"
        case .device: return "laptopcomputer"
        case .trustedDevices: return "checkmark.shield"
        case .extra: return "exclamationmark.circle"
        }
    }

    /// Wide enough for every row label in this language, and never narrower than 74.
    private static let labelWidth = max(74, CardRow.Kind.allCases.map {
        ($0.label as NSString).size(withAttributes: [.font: NSFont.preferredFont(forTextStyle: .callout)]).width.rounded(.up)
    }.max() ?? 0)

    private var facts: [Fact] {
        var out: [Fact] = []
        if !card.who.isEmpty { out.append(Fact(symbol: "person", label: nil, value: card.who)) }
        if card.rows.isEmpty {
            out += card.lines.map { Fact(symbol: "info.circle", label: nil, value: $0) }
        } else {
            out += card.rows.map { Fact(symbol: Self.symbol($0.kind), label: $0.label, value: $0.value) }
        }
        return out
    }

    var body: some View {
        if !facts.isEmpty {
            VStack(spacing: 0) {
                ForEach(Array(facts.enumerated()), id: \.offset) { i, f in
                    if i > 0 { Divider().padding(.leading, 40) }
                    HStack(alignment: .firstTextBaseline, spacing: 10) {
                        Image(systemName: f.symbol).foregroundStyle(.secondary).frame(width: 18)
                        if let l = f.label { Text(l).foregroundStyle(.secondary).frame(width: Self.labelWidth, alignment: .leading) }
                        Text(f.value).frame(maxWidth: .infinity, alignment: .leading).textSelection(.enabled)
                    }
                    .font(.callout)
                    .padding(.horizontal, 12)
                    .padding(.vertical, 8)
                }
            }
            .background(Color(nsColor: .controlBackgroundColor), in: RoundedRectangle(cornerRadius: 10))
            .overlay(RoundedRectangle(cornerRadius: 10).strokeBorder(Color.primary.opacity(0.08)))
        }
    }
}

/// One request in a list of several, as a card: its title and ⓘ, its code,
/// its facts and what approving does.
struct CardView: View {
    let card: Card

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack(alignment: .firstTextBaseline) {
                Text(card.title).font(.headline)
                Spacer()
                if !card.ids.isEmpty || !card.rows.isEmpty { IDsButton(ids: (card.rows.isEmpty ? [] : card.lines) + card.ids) }
            }
            if let code = card.code { CodeChips(code: code) }
            if !card.who.isEmpty { Text(String(localized: "Who: \(card.who)", bundle: .module)).font(.callout) }
            if card.rows.isEmpty {
                ForEach(card.lines, id: \.self) { Text($0).font(.callout).foregroundStyle(.secondary).textSelection(.enabled) }
            } else {
                ForEach(Array(card.rows.enumerated()), id: \.offset) { _, row in
                    Text(String(localized: "\(row.label): \(row.value)", bundle: .module, comment: "A card row: its label and its value")).font(.callout).foregroundStyle(.secondary).textSelection(.enabled)
                }
            }
            Text(card.why).font(.callout)
            if let w = card.warning {
                Label(w, systemImage: "exclamationmark.triangle.fill").foregroundStyle(.orange).font(.callout)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(12)
        .background(.quaternary.opacity(0.5), in: RoundedRectangle(cornerRadius: 10))
    }
}

/// A code the person compares with what the other side shows, drawn the way
/// one-time codes are: each group in its own tile, the tiles filling the
/// row, on a tinted block; "Verification code" under the first tile's left edge, what to
/// do under the last one's right edge.
struct CodeChips: View {
    let code: String
    var label = String(localized: "Verification code", bundle: .module)
    var hint = String(localized: "Must match the other side", bundle: .module)

    var body: some View {
        let groups = code.split(separator: "-").map(String.init)
        VStack(spacing: 8) {
            HStack(spacing: 8) {
                ForEach(Array(groups.enumerated()), id: \.offset) { _, group in
                    Text(group)
                        .font(.system(size: 24, weight: .semibold, design: .monospaced))
                        .tracking(2)
                        .frame(maxWidth: .infinity)
                        .padding(.vertical, 10)
                        .background(Color(nsColor: .controlBackgroundColor), in: RoundedRectangle(cornerRadius: 8))
                        .shadow(color: .black.opacity(0.1), radius: 0.5, y: 0.5)
                }
            }
            HStack {
                Text(label).foregroundStyle(.secondary)
                Spacer()
                Text(hint).fontWeight(.medium)
            }
            .font(.caption)
        }
        .padding(10)
        .background(Self.aurora, in: RoundedRectangle(cornerRadius: 12))
        .textSelection(.enabled)
        .accessibilityElement(children: .combine)
        .accessibilityLabel("\(label) \(code)")
    }

    /// The brand's three colours (brand.css --aurora-0..2, light and dark), very pale.
    private static let aurora = LinearGradient(
        colors: [stop(light: (0.03, 0.57, 0.70), dark: (0.13, 0.88, 0.79), lightAlpha: 0.14, darkAlpha: 0.2),
                 stop(light: (0.18, 0.42, 0.94), dark: (0.24, 0.48, 1.00), lightAlpha: 0.12, darkAlpha: 0.18),
                 stop(light: (0.43, 0.24, 0.94), dark: (0.48, 0.38, 1.00), lightAlpha: 0.14, darkAlpha: 0.22)],
        startPoint: .topLeading, endPoint: .bottomTrailing)

    private static func stop(light: (CGFloat, CGFloat, CGFloat), dark: (CGFloat, CGFloat, CGFloat), lightAlpha: CGFloat, darkAlpha: CGFloat) -> Color {
        Color(nsColor: NSColor(name: nil) { appearance in
            let isDark = appearance.bestMatch(from: [.darkAqua, .aqua]) == .darkAqua
            let c = isDark ? dark : light
            return NSColor(red: c.0, green: c.1, blue: c.2, alpha: isDark ? darkAlpha : lightAlpha)
        })
    }
}

/// ⓘ: a card's identifiers, in a popover.
struct IDsButton: View {
    let ids: [String]
    @State private var shown = false

    var body: some View {
        Button { shown.toggle() } label: { Image(systemName: "info.circle") }
            .buttonStyle(.borderless)
            .help(Text("Technical Details", bundle: .module))
            .accessibilityLabel(Text("Technical Details", bundle: .module))
            .popover(isPresented: $shown, arrowEdge: .bottom) {
                VStack(alignment: .leading, spacing: 6) {
                    ForEach(ids, id: \.self) { Text($0).font(.callout).fixedSize(horizontal: false, vertical: true) }
                }
                .frame(width: 320, alignment: .leading)
                .textSelection(.enabled)
                .padding(12)
            }
    }
}

/// The Touch ID prompt, embedded in the panel instead of a system sheet.
/// It draws at its control size whatever its frame: small is 32 points
/// (regular, the default, is 64 and spills out of the bar).
struct TouchIDView: NSViewRepresentable {
    static let size = NSControl.ControlSize.small
    static let side: CGFloat = 32
    let context: LAContext

    func makeNSView(context _: Context) -> LAAuthenticationView { LAAuthenticationView(context: context, controlSize: Self.size) }
    func updateNSView(_: LAAuthenticationView, context _: Context) {}
}

struct NoticeView: View {
    @EnvironmentObject var model: PanelModel
    let title: String
    let lines: [String]

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Label(title, systemImage: "xmark.octagon.fill").font(.title3.bold()).foregroundStyle(.red)
            ForEach(lines, id: \.self) { Text($0).font(.callout).textSelection(.enabled) }
            HStack {
                Spacer()
                Button(String(localized: "OK", bundle: .module)) { model.onDismiss() }.keyboardShortcut(.defaultAction)
            }
        }
        .padding(20)
        .frame(width: 440)
    }
}

/// After approving a device, or a Mac joining as a machine only: the two
/// digits the person types on it, which prove to it that this approval is
/// theirs (docs/trusted-devices.md 4.2).
struct MatchView: View {
    @EnvironmentObject var model: PanelModel
    let match: Confirmation.JoinMatch

    var body: some View {
        VStack(spacing: 16) {
            VStack(spacing: 6) {
                Image(systemName: "lock.shield.fill").font(.system(size: 30)).foregroundStyle(.tint).padding(.bottom, 2)
                Text(match.label.map { String(localized: "Enter these two digits on “\($0)”", bundle: .module) }
                     ?? (match.machine ? String(localized: "Enter these two digits on the new machine", bundle: .module)
                         : String(localized: "Enter these two digits on the new device", bundle: .module))).font(.title3.weight(.semibold))
                Text(match.machine ? String(localized: "Once you do, it joins as a machine: it runs tasks and approves nothing.", bundle: .module)
                     : String(localized: "Once you do, it becomes a trusted device, just like this Mac.", bundle: .module)).font(.callout).foregroundStyle(.secondary)
            }
            CodeChips(code: match.digits.map(String.init).joined(separator: "-"), label: String(localized: "Two digits", bundle: .module),
                      hint: match.label.map { String(localized: "Enter on “\($0)”", bundle: .module) }
                          ?? (match.machine ? String(localized: "Enter on the new machine", bundle: .module) : String(localized: "Enter on the new device", bundle: .module)))
            Button(String(localized: "Done", bundle: .module)) { model.onDismiss() }.keyboardShortcut(.defaultAction)
        }
        .padding(20)
        .frame(width: 440)
    }
}
