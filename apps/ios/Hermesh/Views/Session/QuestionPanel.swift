import HermeshProto
import HermeshSessions
import SwiftUI

/// The agent's question, rendered inline at the bottom of the conversation stream so the entire
/// page scrolls freely as one surface without occluding context above or clipping options inside
/// a nested scroll box. Multi-question decisions present one item at a time with a clean top stepper.
struct QuestionPanel: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dynamicTypeSize) private var dynamicTypeSize
    let session: Session
    let question: Question
    var isTyping: Binding<Bool> = .constant(false)
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
            VStack(alignment: .leading, spacing: 11) {
                headerRow
                itemBody(it)
                    .id(it.id)
                    .transition(.asymmetric(
                        insertion: .opacity.combined(with: .offset(x: forward ? 14 : -14)),
                        removal: .opacity
                    ))
                actionRow(it: it, curAnswered: curAnswered, nextStep: nextStep)
            }
            .padding(14)
            .background(Palette.reply, in: RoundedRectangle(cornerRadius: 22, style: .continuous))
            .overlay(RoundedRectangle(cornerRadius: 22, style: .continuous).strokeBorder(Palette.separator, lineWidth: 0.5))
            .contentShape(.rect)
            .simultaneousGesture(
                DragGesture(minimumDistance: 42)
                    .onEnded { value in
                        guard multi, !typing, !sending, abs(value.translation.width) > abs(value.translation.height) * 1.3 else { return }
                        if value.translation.width < -42, idx + 1 < count {
                            go(to: idx + 1)
                        } else if value.translation.width > 42, idx > 0 {
                            go(to: idx - 1)
                        }
                    }
            )
            .onChange(of: typing) { _, now in
                isTyping.wrappedValue = now
            }
            .onDisappear {
                advanceTask?.cancel()
                if isTyping.wrappedValue { isTyping.wrappedValue = false }
            }
            .accessibilityElement(children: .contain)
            .accessibilityIdentifier("question")
        }
    }

    @ViewBuilder private var headerRow: some View {
        ViewThatFits(in: .horizontal) {
            HStack(spacing: 8) {
                whoLabel
                Spacer(minLength: 4)
                trailingHeaderControl
            }
            VStack(alignment: .leading, spacing: 6) {
                HStack(spacing: 8) {
                    whoLabel
                    Spacer(minLength: 0)
                }
                trailingHeaderControl
            }
        }
    }

    private var whoLabel: some View {
        HStack(spacing: 6) {
            AgentLogo(id: session.agent, size: 18)
            Text("\(agentTitle(session.agent)) asks")
                .font(.caption.weight(.medium))
                .foregroundStyle(.secondary)
                .lineLimit(1)
            if multi && question.hasExpiresAt {
                Text(verbatim: "·")
                    .font(.caption)
                    .foregroundStyle(.tertiary)
                Text(minutesLeft(until: question.expiresAt.date, now: model.now))
                    .font(.caption.monospacedDigit())
                    .foregroundStyle(.orange)
                    .lineLimit(1)
            }
        }
    }

    @ViewBuilder private var trailingHeaderControl: some View {
        if multi {
            HStack(spacing: 2) {
                Button {
                    go(to: idx - 1)
                } label: {
                    Image(systemName: "chevron.left")
                        .font(.caption.weight(.semibold))
                        .frame(width: 26, height: 24)
                        .contentShape(.rect)
                }
                .buttonStyle(.plain)
                .disabled(idx == 0 || sending)
                .accessibilityLabel("Previous")
                .accessibilityIdentifier("question-previous")

                Text(verbatim: "\(idx + 1)/\(count)")
                    .font(.caption.weight(.semibold).monospacedDigit())
                    .padding(.horizontal, 4)
                    .accessibilityIdentifier("question-step")

                Button {
                    go(to: idx + 1)
                } label: {
                    Image(systemName: "chevron.right")
                        .font(.caption.weight(.semibold))
                        .frame(width: 26, height: 24)
                        .contentShape(.rect)
                }
                .buttonStyle(.plain)
                .disabled(idx + 1 >= count || sending)
                .accessibilityLabel("Next")
                .accessibilityIdentifier("question-next")
            }
            .padding(.horizontal, 4).padding(.vertical, 2)
            .background(Palette.fill2, in: Capsule())
        } else if question.hasExpiresAt {
            Text(minutesLeft(until: question.expiresAt.date, now: model.now))
                .font(.caption.monospacedDigit())
                .foregroundStyle(.orange)
                .lineLimit(1)
                .accessibilityHint("If no one answers by then, it decides by itself")
        }
    }

    @ViewBuilder private func itemBody(_ it: Hermesh_Session_V1_QuestionItem) -> some View {
        let cleanHeader = it.header.trimmingCharacters(in: .whitespacesAndNewlines)
        let cleanText = it.text.trimmingCharacters(in: .whitespacesAndNewlines)
        VStack(alignment: .leading, spacing: 9) {
            if !cleanHeader.isEmpty && cleanHeader != cleanText {
                Text(verbatim: cleanHeader)
                    .font(.caption.weight(.medium))
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }

            questionPrompt(it)
                .fixedSize(horizontal: false, vertical: true)

            if !it.options.isEmpty {
                optionsStack(it)
            }

            if it.options.isEmpty || it.allowOther {
                customField(it)
            }
        }
    }

    private func questionPrompt(_ it: Hermesh_Session_V1_QuestionItem) -> Text {
        var out = Text(it.text).font(.subheadline.weight(.semibold))
        if it.multiSelect {
            let space = Text(verbatim: " ").font(.footnote)
            let tag = Text("Select multiple").font(.footnote).foregroundStyle(.secondary)
            out = Text("\(out)\(space)\(tag)")
        }
        if it.optional {
            let opt = Text(" (optional)").font(.footnote).foregroundStyle(.secondary)
            out = Text("\(out)\(opt)")
        }
        return out
    }

    @ViewBuilder private func optionsStack(_ it: Hermesh_Session_V1_QuestionItem) -> some View {
        VStack(alignment: .leading, spacing: 7) {
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
                        VStack(alignment: .leading, spacing: 3) {
                            RecommendedOptionText(parsed: parsed, selected: on)
                                .foregroundStyle(.primary)
                                .fixedSize(horizontal: false, vertical: true)
                            if !o.description_p.isEmpty {
                                Text(o.description_p)
                                    .font(.footnote)
                                    .foregroundStyle(.secondary)
                                    .fixedSize(horizontal: false, vertical: true)
                            }
                        }
                        .frame(maxWidth: .infinity, alignment: .leading)
                    }
                    .padding(.horizontal, 12).padding(.vertical, 10)
                    .background(
                        on ? AnyShapeStyle(Color.accentColor.opacity(0.14)) : AnyShapeStyle(Palette.fill),
                        in: RoundedRectangle(cornerRadius: 15, style: .continuous)
                    )
                    .overlay {
                        RoundedRectangle(cornerRadius: 15, style: .continuous)
                            .strokeBorder(on ? Color.accentColor : Palette.separator, lineWidth: on ? 1.3 : 0.5)
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
                } else if ready {
                    send(decline: false)
                } else {
                    typing = false
                }
            }
            .accessibilityIdentifier("answer-text")
        }
        .padding(.horizontal, 12).padding(.vertical, 10)
        .background(
            trimmed.isEmpty || typing ? AnyShapeStyle(Palette.fill) : AnyShapeStyle(Color.accentColor.opacity(0.12)),
            in: RoundedRectangle(cornerRadius: 14, style: .continuous)
        )
        .overlay {
            RoundedRectangle(cornerRadius: 14, style: .continuous)
                .strokeBorder(
                    typing || !trimmed.isEmpty ? Color.accentColor.opacity(typing ? 0.9 : 0.55) : Palette.separator,
                    lineWidth: typing ? 1.3 : (!trimmed.isEmpty ? 1 : 0.5)
                )
        }
    }

    @ViewBuilder private func actionRow(it: Hermesh_Session_V1_QuestionItem, curAnswered: Bool, nextStep: Int?) -> some View {
        Group {
            if dynamicTypeSize.isAccessibilitySize {
                VStack(spacing: 8) {
                    primaryButton(it: it, curAnswered: curAnswered, nextStep: nextStep)
                    declineButton
                }
            } else {
                ViewThatFits(in: .horizontal) {
                    HStack(spacing: 10) {
                        declineButton
                        primaryButton(it: it, curAnswered: curAnswered, nextStep: nextStep)
                    }
                    VStack(spacing: 8) {
                        primaryButton(it: it, curAnswered: curAnswered, nextStep: nextStep)
                        declineButton
                    }
                }
            }
        }
        .controlSize(.regular)
        .padding(.top, 2)
        .disabled(sending)
    }

    private var declineButton: some View {
        Button {
            send(decline: true)
        } label: {
            Text("Don’t Answer")
                .lineLimit(1)
                .padding(.vertical, 4)
                .frame(maxWidth: .infinity)
        }
        .buttonStyle(.glass)
        .accessibilityIdentifier("decline")
    }

    @ViewBuilder private func primaryButton(it: Hermesh_Session_V1_QuestionItem, curAnswered: Bool, nextStep: Int?) -> some View {
        if !ready, multi, curAnswered, let nextStep {
            Button {
                go(to: nextStep)
            } label: {
                Text("Continue").lineLimit(1).padding(.vertical, 4).frame(maxWidth: .infinity)
            }
            .buttonStyle(.glassProminent)
            .accessibilityIdentifier("question-continue")
        } else if !ready, multi, it.optional, let nextStep {
            Button {
                go(to: nextStep)
            } label: {
                Text("Skip").lineLimit(1).padding(.vertical, 4).frame(maxWidth: .infinity)
            }
            .buttonStyle(.glass)
            .accessibilityIdentifier("question-skip")
        } else {
            Button {
                send(decline: false)
            } label: {
                Text("Answer").lineLimit(1).padding(.vertical, 4).frame(maxWidth: .infinity)
            }
            .buttonStyle(.glassProminent)
            .disabled(!ready)
            .accessibilityIdentifier("answer")
        }
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
