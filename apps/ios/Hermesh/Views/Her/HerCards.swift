import HermeshHer
import HermeshKit
import SwiftUI

/// A secure card under Her's words (docs/her-secure-cards.md §4): a lock,
/// "Secure Card" and the machine it is for, drawn by the app alone, as a
/// message's words can't be. A `connect` card asks to add a connector (or
/// sign in to one again): what the owner types goes into the answer and
/// leaves the page as it is sent; a sign-in then runs on this iPhone. Once
/// it ended, one line stays.
struct HerSecureCard: View {
    @Environment(AppModel.self) private var model
    let chat: HerChat
    let card: HerCard?
    let machine: HerMachine
    @State private var values: [String: String] = [:]
    @State private var sending = false

    var body: some View {
        if let card, let connect = card.connect, let session = chat.session {
            let here = chat.answered[card.id] != nil
            let shown = herCardShown(herCardNow(card, session, answered: chat.answered[card.id], now: model.now), here: here)
            let name = herConnectorName(connect.name)
            Group {
                if case .ended(let outcome, let tools, _) = shown {
                    ended(outcome, tools: tools, name: name)
                } else {
                    form(card, connect, shown: shown, name: name)
                }
            }
            // her waits for the owner to sign in: the browser opens here, once for each answer.
            .task(id: shown == .signIn) {
                if shown == .signIn, !chat.signInStarted.contains(card.id) {
                    chat.signInStarted.insert(card.id)
                    chat.signIn(card)
                }
            }
            // Nothing typed stays once the form is gone.
            .onChange(of: shown) { if case .form = shown {} else { values = [:] } }
        } else {
            Label("Update Hermesh to answer this card", systemImage: "lock").font(.footnote).foregroundStyle(.secondary)
        }
    }

    private func ended(_ outcome: String, tools: Int?, name: String) -> some View {
        let text: String = switch outcome {
        case "done": tools.map { String(localized: "\(name) connected · \($0) tools") } ?? String(localized: "\(name) connected")
        case "failed": String(localized: "Couldn’t connect \(name)")
        case "declined": String(localized: "Declined")
        case "replaced": String(localized: "Replaced by a newer card")
        case "elsewhere": String(localized: "Answered on another device")
        default: String(localized: "Expired")
        }
        return Label(text, systemImage: outcome == "done" ? "checkmark" : "lock")
            .font(.footnote).foregroundStyle(.secondary)
            .accessibilityIdentifier("her-card-ended")
    }

    private func form(_ card: HerCard, _ connect: HerCard.Connect, shown: HerCardShown, name: String) -> some View {
        let busy = sending || shown == .form(note: nil, busy: true)
        let ready = connect.fields.allSatisfy { !(values[$0.id] ?? "").trimmingCharacters(in: .whitespaces).isEmpty }
        return VStack(alignment: .leading, spacing: 10) {
            HStack(spacing: 5) {
                Image(systemName: "lock.fill")
                Text("Secure Card · \(machine.machineName)")
                if card.askedBy == "her" {
                    Label("You didn’t ask for this", systemImage: "exclamationmark.triangle").foregroundStyle(.orange)
                }
            }
            .font(.caption).foregroundStyle(.secondary)
            HStack(alignment: .firstTextBaseline, spacing: 6) {
                Text(connect.reconnect ? String(localized: "Sign in to \(name) again") : String(localized: "Connect \(name)")).font(.headline)
                Text(connect.host).font(.subheadline).foregroundStyle(.secondary).textSelection(.enabled)
            }
            Text("Her can use its tools; what she sends it leaves this machine").font(.subheadline).foregroundStyle(.secondary)
            if shown == .signIn {
                HStack(spacing: 8) {
                    ProgressView()
                    Text("Waiting for you to sign in in the browser…")
                }
                .font(.subheadline).foregroundStyle(.secondary)
                HStack(spacing: 8) {
                    Button { cancel(card, signingIn: true) } label: { Text("Cancel").frame(maxWidth: .infinity) }.buttonStyle(.glass)
                    Button { chat.signIn(card) } label: { Text("Open Sign-In Page Again").frame(maxWidth: .infinity).lineLimit(1).minimumScaleFactor(0.8) }.buttonStyle(.glass)
                }
            } else {
                ForEach(connect.fields, id: \.id) { f in
                    // A secret for her alone: nothing fills, remembers or corrects it.
                    SecureField(f.label, text: Binding(get: { values[f.id] ?? "" }, set: { values[f.id] = $0 }))
                        .textContentType(.oneTimeCode).autocorrectionDisabled().textInputAutocapitalization(.never)
                        .padding(.horizontal, 12).padding(.vertical, 10)
                        .background(Palette.fill2, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
                        .disabled(busy)
                }
                if case .form(let note?, _) = shown { Text(note).font(.footnote).foregroundStyle(.red).textSelection(.enabled) }
                HStack(spacing: 8) {
                    Button { cancel(card, signingIn: false) } label: { Text("Cancel").frame(maxWidth: .infinity) }
                        .buttonStyle(.glass)
                    Button {
                        let typed = Dictionary(uniqueKeysWithValues: connect.fields.map { ($0.id, (values[$0.id] ?? "").trimmingCharacters(in: .whitespaces)) })
                        values = [:]
                        chat.signInStarted.remove(card.id)
                        answer(card, card.approve(typed))
                    } label: {
                        Text(busy ? String(localized: "Connecting…") : connect.reconnect ? String(localized: "Sign In") : String(localized: "Connect")).frame(maxWidth: .infinity)
                    }
                    .buttonStyle(.glassProminent)
                    .disabled(busy || !ready)
                    .accessibilityIdentifier("her-card-connect")
                }
            }
        }
        .controlSize(.large)
        .disabled(!chat.convUp)
        .padding(14)
        .background(Palette.fill, in: RoundedRectangle(cornerRadius: 18, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: 18, style: .continuous).strokeBorder(Palette.separator))
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("her-card")
    }

    private func cancel(_ card: HerCard, signingIn: Bool) {
        if signingIn { chat.stopSignIn(card) }
        answer(card, HerCard.decline)
    }

    private func answer(_ card: HerCard, _ a: JSON) {
        sending = true
        Task {
            await chat.answer(card, a)
            sending = false
        }
    }
}
