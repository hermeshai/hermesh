import HermeshProto
import HermeshSessions
import SwiftUI

/// A session in the menu (docs/her-team.md §16): the coding agent's logo (with a brand-blue orbit while it runs),
/// the title, one mark (an orange dot while it waits for the owner, a red one when it failed, or a muted Git mark
/// when pushed or merged); with the metadata, who sent the work to which agent, the project and the time. A tap opens
/// it; long-press shows the read-only Git delivery preview card alongside Rename, Archive and Delete. No swipe: in the
/// menu a swipe left is the menu's own, to close it.
struct SessionListRow: View {
    @Environment(AppModel.self) private var model
    @Environment(\.colorScheme) private var colorScheme
    let session: Session
    var isChild = false
    var childFirst = false
    var childLast = false
    var twistShut: Bool?
    var collapsedMark: TreeMark?
    var onTwist: (() -> Void)?
    @State private var renaming = false
    @State private var deleting = false

    var body: some View {
        let selected = model.chosenSession == session.sessionID
        let sender = model.senderLine(session, child: isChild)
        let now = model.now
        let base = HStack(spacing: 0) {
            if let twistShut {
                Button {
                    onTwist?()
                } label: {
                    Image(systemName: "chevron.down")
                        .font(.caption2.weight(.semibold))
                        .foregroundStyle(.secondary)
                        .rotationEffect(.degrees(twistShut ? -90 : 0))
                        .frame(width: 26, height: 44)
                        .contentShape(.rect)
                }
                .buttonStyle(.plain)
                .padding(.leading, 4)
                .accessibilityLabel(twistShut ? Text("Expand") : Text("Collapse"))
                .accessibilityIdentifier("twist-\(session.displayTitle)")
            }
            Button { model.open(.session(session.sessionID)) } label: {
                MenuSessionRow(session: session, metadata: model.listView.metadata, selected: selected, isChild: isChild, hasTwist: twistShut != nil, collapsedMark: collapsedMark)
            }
            .buttonStyle(RowStyle(selected: selected))
            .contentShape(.contextMenuPreview, .capsule)
            .accessibilityAddTraits(selected ? .isSelected : [])
            .contextMenu {
                SessionActions(session: session, isChild: isChild, renaming: $renaming, deleting: $deleting)
            } preview: {
                SessionGitPreviewCard(session: session, senderLine: sender, now: now)
                    .environment(\.colorScheme, colorScheme)
                    .preferredColorScheme(colorScheme)
            }
        }
        .padding(.leading, isChild ? 26 : 0)
        .overlay(alignment: .leading) {
            if isChild {
                Rectangle()
                    .fill(Color.primary.opacity(0.12))
                    .frame(width: 1.5)
                    .padding(.leading, 18)
                    .padding(.top, childFirst ? 6 : 0)
                    .padding(.bottom, childLast ? 6 : 0)
            }
        }
        .sessionDialogs(session, renaming: $renaming, deleting: $deleting)

        Group {
            if twistShut != nil {
                base
                    .accessibilityElement(children: .contain)
                    .accessibilityIdentifier("tree-\(session.displayTitle)")
            } else {
                base
            }
        }
        .listRowInsets(EdgeInsets(top: 0, leading: 10, bottom: 0, trailing: 10))
        .listRowSeparator(.hidden)
        .listRowBackground(Color.clear)
    }
}

/// What a menu row shows: the logo, the title and the mark; the metadata line when the view asks for it.
struct MenuSessionRow: View {
    @Environment(AppModel.self) private var model
    @Environment(\.colorScheme) private var colorScheme
    let session: Session
    var metadata = false
    var selected = false
    var isChild = false
    var hasTwist = false
    var collapsedMark: TreeMark?

    var body: some View {
        let running = session.isActive && !session.needsAttention
        let paused = !(model.drawerOpen || model.menuInView)
        let logoSize: CGFloat = isChild ? 19 : 22
        HStack(alignment: .top, spacing: isChild ? 10 : 12) {
            AgentLogo(id: session.agent, size: logoSize, running: running, paused: paused).frame(width: isChild ? 24 : 28, height: logoSize)
            VStack(alignment: .leading, spacing: 2) {
                HStack(spacing: 8) {
                    // One running has no trailing mark: its logo has a brand-blue orbit and its title shimmers.
                    Text(session.displayTitle)
                        .font(isChild ? .subheadline : .body)
                        .foregroundStyle(session.status == .archived ? .secondary : .primary)
                        .lineLimit(1)
                        .shimmer(running || session.titleGenerating, paused: paused)
                        .frame(maxWidth: .infinity, alignment: .leading)
                    mark
                }
                if metadata {
                    HStack(spacing: 8) {
                        Text(model.senderLine(session, child: isChild)).lineLimit(1).frame(maxWidth: .infinity, alignment: .leading)
                        time.monospacedDigit()
                    }
                    .font(.footnote).foregroundStyle(.secondary)
                }
            }
        }
        .padding(.leading, hasTwist ? 4 : (isChild ? 12 : 16))
        .padding(.trailing, 16)
        .padding(.vertical, isChild ? (metadata ? 6 : 8) : (metadata ? 8 : 11))
        .frame(minHeight: isChild ? 38 : 44)
        .contentShape(.rect)
        .accessibilityElement(children: .combine)
        .accessibilityValue(state.map { Text($0) } ?? Text(""))
        .accessibilityIdentifier("session-\(session.displayTitle)")
    }

    /// One mark: a shut tree shows its most urgent mark (D12); else orange while waiting, red when failed, muted Git glyph when pushed or merged, nothing while running or otherwise done.
    @ViewBuilder private var mark: some View {
        if let collapsedMark {
            Circle().fill(collapsedMark == .waiting ? .orange : .red).frame(width: 9, height: 9).accessibilityHidden(true)
        } else if session.status == .waitingInput || session.isFailed {
            Circle().fill(session.status == .waitingInput ? .orange : .red).frame(width: 9, height: 9).accessibilityHidden(true)
        } else if session.status == .unspecified {
            Text(statusTitle(session.status)).font(.footnote).foregroundStyle(.secondary) // its machine cannot be read now
        } else if let gitMark = session.gitMark {
            let color = gitMark == .pushed ? GitPalette.pushed(colorScheme) : GitPalette.merged(colorScheme)
            let opacity = session.status == .archived ? 0.52 : selected ? 0.88 : 0.74
            Group {
                if gitMark == .pushed {
                    GitPushedIcon().stroke(color, style: StrokeStyle(lineWidth: 1.35, lineCap: .round, lineJoin: .round))
                } else {
                    GitMergedIcon().stroke(color, style: StrokeStyle(lineWidth: 1.35, lineCap: .round, lineJoin: .round))
                }
            }
            .frame(width: 15, height: 15)
            .opacity(opacity)
            .accessibilityHidden(true)
        }
    }

    /// The mark in words, for VoiceOver.
    private var state: String? {
        if let collapsedMark {
            return collapsedMark == .waiting ? statusTitle(.waitingInput) : String(localized: "Failed")
        }
        if session.isFailed && session.status != .crashed && session.status != .waitingInput { return String(localized: "Failed") }
        switch session.status {
        case .waitingInput, .crashed, .preparing, .queued, .running: return statusTitle(session.status)
        default:
            if let gitMark = session.gitMark {
                return gitMark == .pushed ? String(localized: "Pushed") : String(localized: "Merged")
            }
            return nil
        }
    }

    /// How long a running turn has run, else when the session last changed.
    @ViewBuilder private var time: some View {
        if session.status == .running, let start = session.turnStartedDate {
            Text(start, style: .timer)
        } else {
            Text(listTime(session.updatedDate, now: model.now))
        }
    }
}

/// Pure read-only long-press preview card matching the Mac sidebar's `SessionHoverCard`:
/// full untruncated title (up to 3 lines), sender/agent/project/time metadata, and local
/// Git delivery status (branch → target, 5-square diff bar, latest session commit, hint).
struct SessionGitPreviewCard: View {
    @Environment(\.colorScheme) private var colorScheme
    let session: Session
    let senderLine: String
    let now: Date
    var showExcerpt = false

    private var timeText: String {
        if session.status == .running {
            let start = session.turnStartedDate ?? session.createdDate
            return clockText(max(0, now.timeIntervalSince(start)))
        }
        return listTime(session.updatedDate, now: now)
    }

    var body: some View {
        let gs = session.gitStatus
        let gitState = session.hasGitStatus ? gs.state : .unspecified
        let branch = session.effectiveBranch
        VStack(alignment: .leading, spacing: 10) {
            HStack(alignment: .top, spacing: 10) {
                AgentLogo(id: session.agent, size: 20).padding(.top, 1)
                VStack(alignment: .leading, spacing: 3) {
                    Text(session.displayTitle)
                        .font(.subheadline.weight(.semibold))
                        .foregroundStyle(.primary)
                        .lineLimit(3)
                        .fixedSize(horizontal: false, vertical: true)
                        .accessibilityIdentifier("session-git-preview-title")
                    Text("\(senderLine) · \(timeText)")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .lineLimit(1)
                        .monospacedDigit()
                }
                .frame(maxWidth: .infinity, alignment: .leading)
            }
            if showExcerpt, let excerpt = session.searchExcerpt {
                excerptBox(excerpt)
            }
            if gitState != .unspecified && gitState != .clean {
                gitBox(gs: gs, state: gitState)
            } else if showExcerpt && !branch.isEmpty {
                HStack(spacing: 5) {
                    GitBranchIcon()
                        .stroke(.tertiary, style: StrokeStyle(lineWidth: 1.3, lineCap: .round, lineJoin: .round))
                        .frame(width: 12.5, height: 12.5)
                        .accessibilityHidden(true)
                    gitRefPill(branch)
                }
            }
        }
        .padding(.horizontal, 14)
        .padding(.vertical, 13)
        .frame(width: 308, alignment: .leading)
        .background(Color(uiColor: .secondarySystemGroupedBackground))
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("session-git-preview")
    }

    @ViewBuilder private func excerptBox(_ excerpt: SessionExcerpt) -> some View {
        let dark = colorScheme == .dark
        let boxShape = RoundedRectangle(cornerRadius: 9, style: .continuous)
        let (title, tone, bg, border): (String, Color, Color, Color) = switch excerpt.kind {
        case .waiting:
            (String(localized: "Needs You"), .orange, Color.orange.opacity(dark ? 0.14 : 0.10), Color.orange.opacity(0.22))
        case .failed:
            (String(localized: "Failed"), .red, Color.red.opacity(dark ? 0.14 : 0.09), Color.red.opacity(0.20))
        case .running:
            (String(localized: "In Progress"), .secondary, Color.primary.opacity(dark ? 0.06 : 0.045), Color.primary.opacity(0.08))
        case .latest:
            (String(localized: "Latest"), .secondary, Color.primary.opacity(dark ? 0.06 : 0.045), Color.primary.opacity(0.08))
        }
        VStack(alignment: .leading, spacing: 3) {
            Text(title)
                .font(.caption2.weight(.semibold))
                .foregroundStyle(tone)
            Text(excerpt.text)
                .font(.caption)
                .foregroundStyle(.primary)
                .lineLimit(3)
                .fixedSize(horizontal: false, vertical: true)
        }
        .padding(.horizontal, 10)
        .padding(.vertical, 8)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(bg, in: boxShape)
        .overlay { boxShape.strokeBorder(border, lineWidth: 0.5) }
    }

    @ViewBuilder private func gitBox(gs: GitStatus, state: GitState) -> some View {
        let spec = gitSpec(gs: gs, state: state)
        let diff = session.progress.diffStat
        let ins = diff.insertions
        let del = diff.deletions
        let showDiff = ins > 0 || del > 0
        let boxShape = RoundedRectangle(cornerRadius: 9, style: .continuous)

        VStack(alignment: .leading, spacing: 6) {
            HStack(spacing: 6) {
                GitStateGlyph(state: state, color: spec.tone)
                    .frame(width: 14, height: 14)
                    .accessibilityHidden(true)
                Text(spec.label)
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(spec.tone)
                    .lineLimit(1)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .accessibilityIdentifier("session-git-preview-state")
                if showDiff {
                    HStack(spacing: 4) {
                        Text("+\(ins)").foregroundStyle(GitPalette.pushed(colorScheme))
                        Text("−\(del)").foregroundStyle(GitPalette.deletion(colorScheme))
                        HStack(spacing: 1.5) {
                            ForEach(Array(diffBarBlocks(insertions: ins, deletions: del).enumerated()), id: \.offset) { _, block in
                                RoundedRectangle(cornerRadius: 1.3, style: .continuous)
                                    .fill(diffBlockColor(block))
                                    .frame(width: 6, height: 6)
                            }
                        }
                        .padding(.leading, 1)
                        .accessibilityHidden(true)
                    }
                    .font(.system(size: 11, weight: .medium, design: .monospaced))
                    .monospacedDigit()
                }
            }
            if !gs.branch.isEmpty {
                HStack(spacing: 5) {
                    GitBranchIcon()
                        .stroke(.tertiary, style: StrokeStyle(lineWidth: 1.3, lineCap: .round, lineJoin: .round))
                        .frame(width: 12.5, height: 12.5)
                        .accessibilityHidden(true)
                    gitRefPill(gs.branch)
                    if !gs.targetRef.isEmpty && gs.targetRef != gs.branch {
                        Text("→")
                            .font(.system(size: 11))
                            .foregroundStyle(.tertiary)
                        gitRefPill(gs.targetRef)
                    }
                }
            }
            if !gs.headCommit.isEmpty {
                HStack(spacing: 5) {
                    GitCommitIcon()
                        .stroke(.tertiary, style: StrokeStyle(lineWidth: 1.3, lineCap: .round, lineJoin: .round))
                        .frame(width: 12.5, height: 12.5)
                        .accessibilityHidden(true)
                    Text(gs.headCommit)
                        .font(.system(size: 11, design: .monospaced))
                        .foregroundStyle(.secondary)
                        .lineLimit(1)
                    if !gs.headSubject.isEmpty {
                        Text(gs.headSubject)
                            .font(.caption)
                            .foregroundStyle(.primary)
                            .lineLimit(1)
                    }
                }
            }
            Text(spec.hint)
                .font(.caption2)
                .foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
        }
        .padding(.horizontal, 10)
        .padding(.vertical, 9)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(spec.background, in: boxShape)
        .overlay { boxShape.strokeBorder(spec.border, lineWidth: 0.5) }
    }

    private func gitRefPill(_ ref: String) -> some View {
        Text(ref)
            .font(.system(size: 11, design: .monospaced))
            .foregroundStyle(.primary)
            .lineLimit(1)
            .truncationMode(.middle)
            .padding(.horizontal, 5)
            .padding(.vertical, 1.5)
            .background(
                Color.primary.opacity(colorScheme == .dark ? 0.08 : 0.055),
                in: RoundedRectangle(cornerRadius: 4, style: .continuous)
            )
    }

    private func diffBlockColor(_ block: DiffBlock) -> Color {
        switch block {
        case .add: GitPalette.pushed(colorScheme)
        case .del: GitPalette.deletion(colorScheme)
        case .empty: Color.primary.opacity(colorScheme == .dark ? 0.14 : 0.12)
        }
    }

    private struct GitBoxSpec {
        let tone: Color
        let background: Color
        let border: Color
        let label: String
        let hint: String
    }

    private func gitSpec(gs: GitStatus, state: GitState) -> GitBoxSpec {
        let dark = colorScheme == .dark
        func semantic(_ c: Color, label: String, hint: String) -> GitBoxSpec {
            GitBoxSpec(
                tone: c,
                background: c.opacity(dark ? 0.095 : 0.07),
                border: c.opacity(0.16),
                label: label,
                hint: hint
            )
        }
        switch state {
        case .pushed:
            let target = gs.targetRef.isEmpty ? "origin" : gs.targetRef
            return semantic(
                GitPalette.pushed(colorScheme),
                label: String(localized: "Committed & pushed"),
                hint: String(localized: "Synced to remote branch \(target)")
            )
        case .merged:
            let target = gs.targetRef.isEmpty ? "main" : gs.targetRef
            return semantic(
                GitPalette.merged(colorScheme),
                label: String(localized: "Merged into \(target)"),
                hint: String(localized: "Changes are already in \(target)")
            )
        case .unpushed:
            let count = max(1, Int(gs.commitsAhead))
            let label = count == 1
                ? String(localized: "1 local commit unpushed")
                : String(localized: "\(count) local commits unpushed")
            return semantic(
                GitPalette.unpushed(colorScheme),
                label: label,
                hint: String(localized: "Committed locally; push when ready")
            )
        case .dirty:
            let rawFiles = gs.dirtyFiles > 0 ? gs.dirtyFiles : session.progress.diffStat.filesChanged
            let count = max(1, Int(rawFiles))
            let label = count == 1
                ? String(localized: "1 uncommitted file")
                : String(localized: "\(count) uncommitted files")
            return semantic(
                GitPalette.dirty(colorScheme),
                label: label,
                hint: String(localized: "Modified in working tree, not yet committed")
            )
        default:
            return GitBoxSpec(
                tone: .secondary,
                background: Color.primary.opacity(dark ? 0.05 : 0.04),
                border: Color.primary.opacity(0.08),
                label: String(localized: "No code changes"),
                hint: String(localized: "Working tree clean, no new commits")
            )
        }
    }
}

/// Muted semantic Git palette harmonized with Mac's desaturated FinxWork / Onyx tokens (`app.css` `--gh-*`).
enum GitPalette {
    static func pushed(_ scheme: ColorScheme) -> Color {
        scheme == .dark
            ? Color(red: 112 / 255, green: 163 / 255, blue: 101 / 255) // #70a365
            : Color(red: 47 / 255, green: 125 / 255, blue: 74 / 255)   // #2f7d4a
    }

    static func merged(_ scheme: ColorScheme) -> Color {
        scheme == .dark
            ? Color(red: 144 / 255, green: 126 / 255, blue: 192 / 255) // #907ec0
            : Color(red: 110 / 255, green: 89 / 255, blue: 165 / 255)  // #6e59a5
    }

    static func unpushed(_ scheme: ColorScheme) -> Color {
        scheme == .dark
            ? Color(red: 116 / 255, green: 154 / 255, blue: 201 / 255) // #749ac9
            : Color(red: 59 / 255, green: 110 / 255, blue: 168 / 255)  // #3b6ea8
    }

    static func dirty(_ scheme: ColorScheme) -> Color {
        scheme == .dark
            ? Color(red: 192 / 255, green: 152 / 255, blue: 84 / 255)  // #c09854
            : Color(red: 158 / 255, green: 103 / 255, blue: 34 / 255)  // #9e6722
    }

    static func deletion(_ scheme: ColorScheme) -> Color {
        scheme == .dark
            ? Color(red: 220 / 255, green: 132 / 255, blue: 138 / 255) // #dc848a
            : Color(red: 159 / 255, green: 50 / 255, blue: 59 / 255)   // #9f323b
    }
}

private struct GitStateGlyph: View {
    let state: GitState
    let color: Color

    var body: some View {
        let stroke = StrokeStyle(lineWidth: 1.4, lineCap: .round, lineJoin: .round)
        switch state {
        case .pushed:
            GitPushedIcon().stroke(color, style: stroke)
        case .merged:
            GitMergedIcon().stroke(color, style: stroke)
        case .unpushed:
            GitUnpushedIcon().stroke(color, style: stroke)
        case .dirty:
            GeometryReader { geo in
                let d = min(geo.size.width, geo.size.height) * (4.2 / 16.0)
                ZStack {
                    GitDirtyFrameIcon().stroke(color, style: stroke)
                    Circle().fill(color).frame(width: d, height: d)
                }
            }
        default:
            GitCheckIcon().stroke(color, style: stroke)
        }
    }
}

/// Octicon-style repository glyphs drawn on the same 16×16 grid as `apps/desktop/src/renderer/src/icons.tsx`.
nonisolated struct GitPushedIcon: Shape {
    func path(in rect: CGRect) -> Path {
        let k = min(rect.width, rect.height) / 16
        func p(_ x: CGFloat, _ y: CGFloat) -> CGPoint { CGPoint(x: rect.minX + x * k, y: rect.minY + y * k) }
        func circle(_ cx: CGFloat, _ cy: CGFloat, _ r: CGFloat, into path: inout Path) {
            path.addEllipse(in: CGRect(x: rect.minX + (cx - r) * k, y: rect.minY + (cy - r) * k, width: 2 * r * k, height: 2 * r * k))
        }
        var path = Path()
        circle(4.5, 3.8, 1.55, into: &path)
        circle(4.5, 12.2, 1.55, into: &path)
        circle(11.5, 12.2, 1.55, into: &path)
        path.move(to: p(4.5, 5.4))
        path.addLine(to: p(4.5, 10.6))
        path.move(to: p(11.5, 10.6))
        path.addLine(to: p(11.5, 5.6))
        path.addCurve(to: p(9.5, 3.8), control1: p(11.5, 4.5), control2: p(10.6, 3.8))
        path.addLine(to: p(8.0, 3.8))
        path.move(to: p(9.5, 2.2))
        path.addLine(to: p(7.8, 3.8))
        path.addLine(to: p(9.5, 5.4))
        return path
    }
}

nonisolated struct GitMergedIcon: Shape {
    func path(in rect: CGRect) -> Path {
        let k = min(rect.width, rect.height) / 16
        func p(_ x: CGFloat, _ y: CGFloat) -> CGPoint { CGPoint(x: rect.minX + x * k, y: rect.minY + y * k) }
        func circle(_ cx: CGFloat, _ cy: CGFloat, _ r: CGFloat, into path: inout Path) {
            path.addEllipse(in: CGRect(x: rect.minX + (cx - r) * k, y: rect.minY + (cy - r) * k, width: 2 * r * k, height: 2 * r * k))
        }
        var path = Path()
        circle(4.5, 3.8, 1.55, into: &path)
        circle(4.5, 12.2, 1.55, into: &path)
        circle(11.8, 8.8, 1.55, into: &path)
        path.move(to: p(4.5, 5.4))
        path.addLine(to: p(4.5, 10.6))
        path.move(to: p(4.5, 5.4))
        path.addCurve(to: p(10.2, 8.8), control1: p(4.5, 7.9), control2: p(6.9, 8.8))
        return path
    }
}

private nonisolated struct GitUnpushedIcon: Shape {
    func path(in rect: CGRect) -> Path {
        let k = min(rect.width, rect.height) / 16
        func p(_ x: CGFloat, _ y: CGFloat) -> CGPoint { CGPoint(x: rect.minX + x * k, y: rect.minY + y * k) }
        var path = Path()
        path.addEllipse(in: CGRect(x: rect.minX + (8 - 1.75) * k, y: rect.minY + (11.6 - 1.75) * k, width: 3.5 * k, height: 3.5 * k))
        path.move(to: p(3.2, 11.6)); path.addLine(to: p(6.2, 11.6))
        path.move(to: p(9.8, 11.6)); path.addLine(to: p(12.8, 11.6))
        path.move(to: p(8.0, 8.2)); path.addLine(to: p(8.0, 2.6))
        path.move(to: p(5.5, 5.1)); path.addLine(to: p(8.0, 2.6)); path.addLine(to: p(10.5, 5.1))
        return path
    }
}

private nonisolated struct GitDirtyFrameIcon: Shape {
    func path(in rect: CGRect) -> Path {
        let k = min(rect.width, rect.height) / 16
        var path = Path()
        path.addRoundedRect(
            in: CGRect(x: rect.minX + 2.4 * k, y: rect.minY + 2.4 * k, width: 11.2 * k, height: 11.2 * k),
            cornerSize: CGSize(width: 2.4 * k, height: 2.4 * k)
        )
        return path
    }
}

nonisolated struct GitBranchIcon: Shape {
    func path(in rect: CGRect) -> Path {
        let k = min(rect.width, rect.height) / 16
        func p(_ x: CGFloat, _ y: CGFloat) -> CGPoint { CGPoint(x: rect.minX + x * k, y: rect.minY + y * k) }
        func circle(_ cx: CGFloat, _ cy: CGFloat, _ r: CGFloat, into path: inout Path) {
            path.addEllipse(in: CGRect(x: rect.minX + (cx - r) * k, y: rect.minY + (cy - r) * k, width: 2 * r * k, height: 2 * r * k))
        }
        var path = Path()
        circle(4.8, 3.8, 1.5, into: &path)
        circle(4.8, 12.2, 1.5, into: &path)
        circle(11.2, 5.4, 1.5, into: &path)
        path.move(to: p(4.8, 5.3))
        path.addLine(to: p(4.8, 10.7))
        path.move(to: p(11.2, 6.9))
        path.addCurve(to: p(4.8, 10.6), control1: p(11.2, 9.3), control2: p(9.0, 9.9))
        return path
    }
}

private nonisolated struct GitCommitIcon: Shape {
    func path(in rect: CGRect) -> Path {
        let k = min(rect.width, rect.height) / 16
        func p(_ x: CGFloat, _ y: CGFloat) -> CGPoint { CGPoint(x: rect.minX + x * k, y: rect.minY + y * k) }
        var path = Path()
        path.addEllipse(in: CGRect(x: rect.minX + (8 - 2.3) * k, y: rect.minY + (8 - 2.3) * k, width: 4.6 * k, height: 4.6 * k))
        path.move(to: p(1.8, 8.0)); path.addLine(to: p(5.7, 8.0))
        path.move(to: p(10.3, 8.0)); path.addLine(to: p(14.2, 8.0))
        return path
    }
}

private nonisolated struct GitCheckIcon: Shape {
    func path(in rect: CGRect) -> Path {
        let k = min(rect.width, rect.height) / 16
        func p(_ x: CGFloat, _ y: CGFloat) -> CGPoint { CGPoint(x: rect.minX + x * k, y: rect.minY + y * k) }
        var path = Path()
        path.move(to: p(3.4, 8.4))
        path.addLine(to: p(6.4, 11.4))
        path.addLine(to: p(12.6, 4.6))
        return path
    }
}

/// Rename, Archive (or Unarchive), and Delete after a divider: a row's
/// long-press menu and the session page's ⋯ menu.
struct SessionActions: View {
    @Environment(AppModel.self) private var model
    let session: Session
    var isChild = false
    @Binding var renaming: Bool
    @Binding var deleting: Bool

    var body: some View {
        if session.status != .archived && !isChild {
            Button(session.pinned ? "Unpin" : "Pin", systemImage: session.pinned ? "pin.slash" : "pin") {
                Task { await model.setPinned(session, !session.pinned) }
            }
        }
        Button("Rename", systemImage: "pencil") { renaming = true }
        if session.status == .archived {
            Button("Unarchive", systemImage: "tray.and.arrow.up") { Task { await model.setArchived(session, false) } }
        } else {
            let archivable = model.canArchive(session)
            let treeBusy = session.isArchivable && !archivable
            if treeBusy {
                Button {
                    Task { await model.setArchived(session, true) }
                } label: {
                    Text("Archive")
                    Text("Tasks are still running")
                    Image(systemName: "archivebox")
                }
                .disabled(true)
                .accessibilityIdentifier("archive-still-running")
            } else {
                Button("Archive", systemImage: "archivebox") {
                    Task { await model.setArchived(session, true) }
                }
                .disabled(!archivable)
                .accessibilityIdentifier("session-archive")
            }
        }
        Divider()
        Button("Delete…", systemImage: "trash", role: .destructive) { deleting = true }
    }
}

extension View {
    /// The rename field and the delete confirmation SessionActions asks for.
    func sessionDialogs(_ s: Session, renaming: Binding<Bool>, deleting: Binding<Bool>) -> some View {
        modifier(SessionDialogs(session: s, renaming: renaming, deleting: deleting))
    }
}

private struct SessionDialogs: ViewModifier {
    @Environment(AppModel.self) private var model
    let session: Session
    @Binding var renaming: Bool
    @Binding var deleting: Bool
    @State private var title = ""

    func body(content: Content) -> some View {
        content
            .alert("Rename Session", isPresented: $renaming) {
                TextField("Session title", text: $title)
                    .accessibilityIdentifier("rename-field")
                Button("Cancel", role: .cancel) {}
                Button("Rename") { Task { await model.rename(session, to: title) } }
                    .accessibilityIdentifier("rename-confirm")
            }
            .onChange(of: renaming) { _, on in if on { title = session.displayTitle } }
            .confirmationDialog(Text("Delete “\(session.displayTitle)”?"), isPresented: $deleting, titleVisibility: .visible) {
                Button("Delete", role: .destructive) { Task { await model.delete(session) } }
                    .accessibilityIdentifier("delete-confirm")
            } message: {
                Text(deleteMessage)
            }
    }

    private var deleteMessage: String {
        let gone = session.branch.isEmpty
            ? String(localized: "Its record and conversation are deleted for good.")
            : String(localized: "Its record, conversation and worktree are deleted for good. The agent’s changes stay on the branch \(session.branch).")
        var parts = [gone]
        if session.isActive { parts.append(String(localized: "Its turn in progress is interrupted first.")) }
        if session.isHerConversation { parts.append(String(localized: "The tasks it sent are kept.")) }
        return parts.joined(separator: " ")
    }
}

/// The list's view menu: Group, Sort and Show, each a submenu saying what it is set to.
struct ViewMenu: View {
    @Environment(AppModel.self) private var model
    /// In the system's bar: its symbol alone, the bar drawing the glass as for its other buttons.
    var inBar = false

    var body: some View {
        // The bar draws its buttons' glass and colour: the plain style only for the menu's own glass.
        Group {
            if inBar { menu } else { menu.buttonStyle(.plain) }
        }
        .accessibilityLabel("View Options")
        .accessibilityIdentifier("view-options")
    }

    private var menu: some View {
        @Bindable var model = model
        return Menu {
            Menu {
                Picker("Group", selection: $model.listView.group) {
                    ForEach(SessionGrouping.allCases, id: \.self) { g in Text(groupName(g)).tag(g) }
                }
            } label: {
                Text("Group")
                Text(groupName(model.listView.group))
            }
            Menu {
                Picker("Sort By", selection: $model.listView.sort) {
                    Text("Default (In Progress by Created)").tag(SessionSort.default)
                    Text("Created").tag(SessionSort.created)
                    Text("Last Active").tag(SessionSort.updated)
                }
                .pickerStyle(.inline)
                Picker("Order", selection: $model.listView.newestFirst) {
                    Text("Newest First").tag(true)
                    Text("Oldest First").tag(false)
                }
                .pickerStyle(.inline)
                if !model.activeOrder.isEmpty || !model.pinnedOrder.isEmpty {
                    Divider()
                    Button("Reset Custom Order") {
                        model.activeOrder = []
                        model.pinnedOrder = []
                    }
                }
            } label: {
                Text("Sort")
                Text(model.listView.newestFirst ? "Newest First" : "Oldest First")
            }
            Menu {
                Toggle("Metadata", isOn: $model.listView.metadata)
                Toggle("Nest subtasks", isOn: $model.listView.nest)
            } label: {
                Text("Show")
                Text(showValue)
            }
        } label: {
            if inBar {
                Image(systemName: "arrow.up.arrow.down")
            } else {
                // The menu's own glass, as its Search beside it, a size smaller: in the text's colour, not the tint.
                Image(systemName: "arrow.up.arrow.down").font(.subheadline.weight(.semibold)).foregroundStyle(.primary)
                    .frame(width: 36, height: 36)
                    .glassEffect(.regular.interactive(), in: .circle)
                    .contentShape(.circle)
            }
        }
    }

    private var showValue: LocalizedStringKey {
        switch (model.listView.metadata, model.listView.nest) {
        case (true, true): "Metadata, nested"
        case (true, false): "Metadata"
        case (false, true): "Nested"
        case (false, false): "Title Only"
        }
    }

    private func groupName(_ g: SessionGrouping) -> LocalizedStringKey {
        switch g {
        case .status: "By Status"
        case .date: "By Date"
        case .project: "By Project"
        case .agent: "By Agent"
        case .machine: "By Machine"
        }
    }
}
