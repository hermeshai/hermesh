import HermeshProto
import HermeshSessions
import SwiftUI

/// The agent's question, over the bottom of the conversation: its options,
/// a free answer, and "Don't Answer" (let it decide by itself). Only Answer
/// is tinted.
struct QuestionPanel: View {
    @Environment(AppModel.self) private var model
    let session: Session
    let question: Question
    @State private var picked: [String: Set<String>] = [:]
    @State private var text: [String: String] = [:]
    @State private var sending = false
    @FocusState private var typing: Bool

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack(spacing: 8) {
                AgentLogo(id: session.agent, size: 22)
                Text("\(agentTitle(session.agent)) asks").font(.subheadline).foregroundStyle(.secondary)
                Spacer()
                if question.hasExpiresAt {
                    Text(minutesLeft(until: question.expiresAt.date, now: model.now)).font(.footnote).foregroundStyle(.orange)
                        .accessibilityHint("If no one answers by then, it decides by itself")
                }
            }
            ScrollView {
                VStack(alignment: .leading, spacing: 14) {
                    ForEach(question.items, id: \.id) { item($0) }
                }
            }
            .scrollBounceBehavior(.basedOnSize)
            .frame(maxHeight: 340)
            HStack(spacing: 10) {
                Button("Don’t Answer") { send(decline: true) }
                    .buttonStyle(.glass).frame(maxWidth: .infinity)
                    .accessibilityIdentifier("decline")
                Button {
                    send(decline: false)
                } label: {
                    Text("Answer").frame(maxWidth: .infinity)
                }
                .buttonStyle(.glassProminent).frame(maxWidth: .infinity)
                .disabled(!ready)
                .accessibilityIdentifier("answer")
            }
            .controlSize(.large)
            .disabled(sending)
        }
        .padding(18)
        .glassEffect(.regular, in: RoundedRectangle(cornerRadius: 36, style: .continuous))
        .padding(.horizontal, 10).padding(.bottom, 4)
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("question")
    }

    @ViewBuilder private func item(_ it: Hermesh_Session_V1_QuestionItem) -> some View {
        VStack(alignment: .leading, spacing: 8) {
            if !it.header.isEmpty { Text(it.header).font(.caption).foregroundStyle(.secondary) }
            Text(it.text).font(.headline).fixedSize(horizontal: false, vertical: true)
            ForEach(it.options, id: \.label) { o in
                let on = picked[it.id, default: []].contains(o.label)
                Button {
                    var s = picked[it.id, default: []]
                    if it.multiSelect { if on { s.remove(o.label) } else { s.insert(o.label) } } else { s = on ? [] : [o.label] }
                    picked[it.id] = s
                } label: {
                    HStack(spacing: 10) {
                        Image(systemName: on ? (it.multiSelect ? "checkmark.square.fill" : "checkmark.circle.fill") : (it.multiSelect ? "square" : "circle"))
                            .foregroundStyle(on ? Color.accentColor : .secondary)
                        Text(o.label).frame(maxWidth: .infinity, alignment: .leading)
                    }
                    .padding(.horizontal, 14).padding(.vertical, 11)
                    .background(on ? AnyShapeStyle(Color.accentColor.opacity(0.12)) : AnyShapeStyle(.fill.tertiary), in: RoundedRectangle(cornerRadius: 16, style: .continuous))
                }
                .buttonStyle(.plain)
                .accessibilityAddTraits(on ? .isSelected : [])
            }
            if it.options.isEmpty || it.allowOther {
                Group {
                    if it.secret {
                        SecureField("Your answer (secret: only the agent gets it, nothing records it)", text: binding(it.id))
                    } else {
                        TextField(it.options.isEmpty ? "Write your answer…" : "Or write your own answer…", text: binding(it.id), axis: .vertical)
                    }
                }
                .focused($typing)
                .padding(.horizontal, 14).padding(.vertical, 11)
                .background(.fill.tertiary, in: RoundedRectangle(cornerRadius: 16, style: .continuous))
                .accessibilityIdentifier("answer-text")
            }
        }
    }

    private func binding(_ id: String) -> Binding<String> {
        Binding(get: { text[id] ?? "" }, set: { text[id] = $0 })
    }

    /// Every item that must be answered has an answer.
    private var ready: Bool {
        question.items.allSatisfy { it in
            it.optional || !(picked[it.id] ?? []).isEmpty || !(text[it.id] ?? "").trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
        }
    }

    private func send(decline: Bool) {
        typing = false
        sending = true
        let answers: [Answer] = decline ? [] : question.items.map { it in
            var a = Answer()
            a.itemID = it.id
            a.selected = Array(picked[it.id] ?? []).sorted()
            a.text = (text[it.id] ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
            return a
        }
        Task {
            _ = await model.answer(session, answers: answers, decline: decline)
            sending = false
        }
    }
}
