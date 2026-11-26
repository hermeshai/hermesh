import HermeshProto
import HermeshSessions
import SwiftUI

/// The agent's question, over the bottom of the conversation: presented one
/// item at a time so multi-question decisions never stack into a nested scroll
/// wall. Single-choice picks advance automatically; the top pager and segmented
/// bars let the owner revisit and change any answer before submitting.
struct QuestionPanel: View {
    @Environment(AppModel.self) private var model
    let session: Session
    let question: Question
    /// How the answer goes, when not as a session's (a v2 thread answers its request).
    var answer: (([Answer], Bool) async -> Void)? = nil

    @State private var step = 0
    @State private var forward = true
    @State private var picked: [String: Set<String>] = [:]
    @State private var text: [String: String] = [:]
    @State private var sending = false
    @State private var advanceTask: Task<Void, Never>?
    @FocusState private var typing: Bool

    private var count: Int { question.items.count }
    private var idx: Int { count > 0 ? min(step, count - 1) : 0 }
    private var multi: Bool { count > 1 }
    private var ready: Bool { isQuestionReady(question.items, picked: picked, text: text) }

    var body: some View {
        if let it = count > 0 ? question.items[idx] : nil {
            let curAnswered = isQuestionItemAnswered(it, picked: picked, text: text)
            let nextStep = nextManualQuestionStep(question.items, picked: picked, text: text, from: idx)
            VStack(alignment: .leading, spacing: 10) {
                headerRow(it)
                if multi {
                    segmentedTrack
                }
                itemBody(it)
                    .id(it.id)
                    .transition(.asymmetric(
                        insertion: .opacity.combined(with: .offset(x: forward ? 14 : -14)),
                        removal: .opacity
                    ))
                actionRow(it: it, curAnswered: curAnswered, nextStep: nextStep)
            }
            .padding(16)
            .glassEffect(.regular, in: RoundedRectangle(cornerRadius: 34, style: .continuous))
            .padding(.horizontal, 10).padding(.bottom, 4)
            .contentShape(.rect)
            .simultaneousGesture(
                DragGesture(minimumDistance: 42)
                    .onEnded { value in
                        guard multi, !typing, !sending, abs(value.translation.width) > abs(value.translation.height) else { return }
                        if value.translation.width < -42, idx + 1 < count {
                            go(to: idx + 1)
                        } else if value.translation.width > 42, idx > 0 {
                            go(to: idx - 1)
                        }
                    }
            )
            .onDisappear {
                advanceTask?.cancel()
            }
            .accessibilityElement(children: .contain)
            .accessibilityIdentifier("question")
        }
    }

    @ViewBuilder private func headerRow(_ it: Hermesh_Session_V1_QuestionItem) -> some View {
        HStack(spacing: 7) {
            AgentLogo(id: session.agent, size: 20)
            Text("\(agentTitle(session.agent)) asks")
                .font(.subheadline)
                .foregroundStyle(.secondary)
                .lineLimit(1)
            if !it.header.isEmpty && it.header != it.text {
                Text(it.header)
                    .font(.caption2.weight(.medium))
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
                    .padding(.horizontal, 8).padding(.vertical, 3)
                    .background(.fill.tertiary, in: Capsule())
            }
            if it.multiSelect {
                Text("Select multiple")
                    .font(.caption2.weight(.semibold))
                    .foregroundStyle(Color.accentColor)
                    .lineLimit(1)
                    .padding(.horizontal, 8).padding(.vertical, 3)
                    .background(Color.accentColor.opacity(0.14), in: Capsule())
            }
            Spacer(minLength: 4)
            if question.hasExpiresAt {
                Text(minutesLeft(until: question.expiresAt.date, now: model.now))
                    .font(.caption.monospacedDigit())
                    .foregroundStyle(.orange)
                    .lineLimit(1)
                    .accessibilityHint("If no one answers by then, it decides by itself")
            }
            if multi {
                HStack(spacing: 2) {
                    Button {
                        go(to: idx - 1)
                    } label: {
                        Image(systemName: "chevron.left")
                            .font(.caption.weight(.semibold))
                            .frame(width: 24, height: 22)
                            .contentShape(.rect)
                    }
                    .buttonStyle(.plain)
                    .disabled(idx == 0 || sending)
                    .accessibilityLabel("Previous")
                    .accessibilityIdentifier("question-previous")

                    Text(verbatim: "\(idx + 1)/\(count)")
                        .font(.caption.weight(.semibold).monospacedDigit())
                        .padding(.horizontal, 3)
                        .accessibilityIdentifier("question-step")

                    Button {
                        go(to: idx + 1)
                    } label: {
                        Image(systemName: "chevron.right")
                            .font(.caption.weight(.semibold))
                            .frame(width: 24, height: 22)
                            .contentShape(.rect)
                    }
                    .buttonStyle(.plain)
                    .disabled(idx + 1 >= count || sending)
                    .accessibilityLabel("Next")
                    .accessibilityIdentifier("question-next")
                }
                .padding(.horizontal, 4).padding(.vertical, 2)
                .background(.fill.tertiary, in: Capsule())
            }
        }
    }

    private var segmentedTrack: some View {
        HStack(spacing: 5) {
            ForEach(Array(question.items.enumerated()), id: \.element.id) { sIdx, stepIt in
                let done = isQuestionItemAnswered(stepIt, picked: picked, text: text)
                let cur = sIdx == idx
                Button {
                    go(to: sIdx)
                } label: {
                    Capsule()
                        .fill(cur ? Color.accentColor : (done ? Color.accentColor.opacity(0.48) : Color.secondary.opacity(0.24)))
                        .frame(height: cur ? 4.5 : 3.5)
                        .frame(maxWidth: .infinity)
                        .padding(.vertical, 4)
                        .contentShape(.rect)
                }
                .buttonStyle(.plain)
                .disabled(sending)
                .accessibilityLabel(Text(verbatim: "\(sIdx + 1) / \(count)"))
                .accessibilityIdentifier("question-seg-\(sIdx)")
            }
        }
    }

    @ViewBuilder private func itemBody(_ it: Hermesh_Session_V1_QuestionItem) -> some View {
        VStack(alignment: .leading, spacing: 8) {
            (Text(it.text).font(.subheadline.weight(.semibold)) + (it.optional ? Text(" (optional)").font(.footnote).foregroundStyle(.secondary) : Text("")))
                .fixedSize(horizontal: false, vertical: true)

            if !it.options.isEmpty {
                let list = optionsStack(it)
                ViewThatFits(in: .vertical) {
                    list
                    ScrollView { list }
                        .scrollBounceBehavior(.basedOnSize)
                        .frame(maxHeight: typing ? 132 : 232)
                }
            }

            if it.options.isEmpty || it.allowOther {
                customField(it)
            }
        }
    }

    @ViewBuilder private func optionsStack(_ it: Hermesh_Session_V1_QuestionItem) -> some View {
        VStack(alignment: .leading, spacing: 6) {
            ForEach(it.options, id: \.label) { o in
                let on = picked[it.id, default: []].contains(o.label)
                let parsed = parseOptionLabel(o.label)
                Button {
                    choose(it, label: o.label)
                } label: {
                    HStack(alignment: .top, spacing: 10) {
                        Image(systemName: on ? (it.multiSelect ? "checkmark.square.fill" : "checkmark.circle.fill") : (it.multiSelect ? "square" : "circle"))
                            .font(.body)
                            .foregroundStyle(on ? Color.accentColor : .secondary)
                            .padding(.top, 1)
                        VStack(alignment: .leading, spacing: 2) {
                            HStack(alignment: .firstTextBaseline, spacing: 6) {
                                if parsed.recommended {
                                    Text("Recommended")
                                        .font(.caption2.weight(.semibold))
                                        .foregroundStyle(on ? .white : Color.accentColor)
                                        .padding(.horizontal, 6).padding(.vertical, 2)
                                        .background(on ? Color.accentColor : Color.accentColor.opacity(0.14), in: RoundedRectangle(cornerRadius: 5, style: .continuous))
                                }
                                Text(parsed.text)
                                    .font(.subheadline.weight(.medium))
                                    .foregroundStyle(.primary)
                            }
                            if !o.description_p.isEmpty {
                                Text(o.description_p)
                                    .font(.footnote)
                                    .foregroundStyle(.secondary)
                            }
                        }
                        .frame(maxWidth: .infinity, alignment: .leading)
                    }
                    .padding(.horizontal, 13).padding(.vertical, 10)
                    .background(
                        on ? AnyShapeStyle(Color.accentColor.opacity(0.13)) : AnyShapeStyle(.fill.tertiary),
                        in: RoundedRectangle(cornerRadius: 16, style: .continuous)
                    )
                    .overlay {
                        if on {
                            RoundedRectangle(cornerRadius: 16, style: .continuous)
                                .strokeBorder(Color.accentColor, lineWidth: 1.3)
                        }
                    }
                }
                .buttonStyle(.plain)
                .disabled(sending)
                .accessibilityAddTraits(on ? .isSelected : [])
            }
        }
    }

    @ViewBuilder private func customField(_ it: Hermesh_Session_V1_QuestionItem) -> some View {
        let trimmed = (text[it.id] ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
        HStack(spacing: 8) {
            Image(systemName: it.secret ? "lock" : "square.and.pencil")
                .font(.footnote)
                .foregroundStyle(trimmed.isEmpty ? Color.secondary : Color.accentColor)
            Group {
                if it.secret {
                    SecureField("Your answer (secret: only the agent gets it, nothing records it)", text: customBinding(it))
                } else {
                    TextField(it.options.isEmpty ? "Write your answer…" : "Or write your own answer…", text: customBinding(it))
                }
            }
            .font(.subheadline)
            .focused($typing)
            .submitLabel(ready ? .send : (multi ? .next : .done))
            .onSubmit {
                if !ready, multi, isQuestionItemAnswered(it, picked: picked, text: text),
                   let next = nextManualQuestionStep(question.items, picked: picked, text: text, from: idx) {
                    go(to: next)
                } else {
                    typing = false
                }
            }
            .accessibilityIdentifier("answer-text")
        }
        .padding(.horizontal, 13).padding(.vertical, 10)
        .background(
            trimmed.isEmpty || typing ? AnyShapeStyle(.fill.tertiary) : AnyShapeStyle(Color.accentColor.opacity(0.12)),
            in: RoundedRectangle(cornerRadius: 15, style: .continuous)
        )
        .overlay {
            if typing || !trimmed.isEmpty {
                RoundedRectangle(cornerRadius: 15, style: .continuous)
                    .strokeBorder(Color.accentColor.opacity(typing ? 0.9 : 0.55), lineWidth: typing ? 1.3 : 1)
            }
        }
    }

    @ViewBuilder private func actionRow(it: Hermesh_Session_V1_QuestionItem, curAnswered: Bool, nextStep: Int?) -> some View {
        HStack(spacing: 10) {
            Button("Don’t Answer") { send(decline: true) }
                .buttonStyle(.glass).frame(maxWidth: .infinity)
                .accessibilityIdentifier("decline")

            if !ready, multi, curAnswered, let nextStep {
                Button {
                    go(to: nextStep)
                } label: {
                    Text("Continue").frame(maxWidth: .infinity)
                }
                .buttonStyle(.glassProminent).frame(maxWidth: .infinity)
                .accessibilityIdentifier("question-continue")
            } else if !ready, multi, it.optional, let nextStep {
                Button {
                    go(to: nextStep)
                } label: {
                    Text("Skip").frame(maxWidth: .infinity)
                }
                .buttonStyle(.glass).frame(maxWidth: .infinity)
                .accessibilityIdentifier("question-skip")
            } else {
                Button {
                    send(decline: false)
                } label: {
                    Text("Answer").frame(maxWidth: .infinity)
                }
                .buttonStyle(.glassProminent).frame(maxWidth: .infinity)
                .disabled(!ready)
                .accessibilityIdentifier("answer")
            }
        }
        .controlSize(.large)
        .disabled(sending)
    }

    private func go(to target: Int) {
        guard target >= 0, target < count, target != idx else { return }
        advanceTask?.cancel()
        typing = false
        forward = target > idx
        withAnimation(.snappy(duration: 0.22)) {
            step = target
        }
    }

    private func choose(_ it: Hermesh_Session_V1_QuestionItem, label: String) {
        guard !sending else { return }
        advanceTask?.cancel()
        typing = false
        applyQuestionChoice(it, label: label, picked: &picked, text: &text)
        if !it.multiSelect, multi, let next = nextAutoQuestionStep(question.items, picked: picked, text: text, from: idx) {
            advanceTask = Task { @MainActor in
                try? await Task.sleep(for: .milliseconds(180))
                guard !Task.isCancelled else { return }
                forward = next > idx
                withAnimation(.snappy(duration: 0.22)) {
                    step = next
                }
            }
        }
    }

    private func customBinding(_ it: Hermesh_Session_V1_QuestionItem) -> Binding<String> {
        Binding(
            get: { text[it.id] ?? "" },
            set: { newValue in
                advanceTask?.cancel()
                applyQuestionCustomText(it, value: newValue, picked: &picked, text: &text)
            }
        )
    }

    private func send(decline: Bool) {
        advanceTask?.cancel()
        typing = false
        sending = true
        let answers: [Answer] = decline ? [] : question.items.filter { isQuestionItemAnswered($0, picked: picked, text: text) }.map { it in
            var a = Answer()
            a.itemID = it.id
            a.selected = it.options.map(\.label).filter { (picked[it.id] ?? []).contains($0) }
            a.text = (text[it.id] ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
            return a
        }
        Task {
            if let answer { await answer(answers, decline) } else { _ = await model.answer(session, answers: answers, decline: decline) }
            sending = false
        }
    }
}
