import PDFKit
import SwiftUI
import WebKit

/// Mermaid diagrams as the Mac app draws them: its Mermaid and its settings
/// (scripts/diagram), run on one page that is never shown. Each diagram comes
/// back as a PDF: the conversation shows a picture made from it, opened large
/// a finer one, and it is shared as it is. Diagrams are drawn one at a time,
/// and the last few are kept.
final class DiagramRenderer: NSObject, WKNavigationDelegate {
    static let shared = DiagramRenderer()

    struct Drawing {
        let pdf: Data
        let image: UIImage
    }

    enum Failure: Error { case unsupported, failed }

    /// The longest source drawn, as on the Mac.
    static let maxLength = 20000

    /// Whether a diagram may be drawn (the Mac's rule): its look comes from the app, never from
    /// the source, and it holds no link, HTML label or click.
    static func valid(_ source: String) -> Bool {
        source.count <= maxLength
            && source.firstMatch(of: #/%%\s*\{|^\s*---|\bclick\s|https?:|data:|javascript:|url\s*\(|\bimg\s*:|\bicon\s*:|@import|<\/?[a-z]/#.ignoresCase().anchorsMatchLineEndings()) == nil
    }

    private var page: WKWebView?
    private var loading: Task<WKWebView, Error>?
    private var loaded: CheckedContinuation<Void, Error>?
    private var kept: [String: Drawing] = [:]
    private var order: [String] = []
    private var last: Task<Void, Never>?

    func cached(_ source: String, dark: Bool) -> Drawing? { kept["\(dark)\u{0}\(source)"] }

    func drawing(_ source: String, dark: Bool) async throws -> Drawing {
        guard Self.valid(source) else { throw Failure.unsupported }
        let key = "\(dark)\u{0}\(source)"
        if let hit = kept[key] { return hit }
        let before = last
        let work = Task { () throws -> Drawing in
            await before?.value
            if let hit = kept[key] { return hit }
            let drawing = try await draw(source, dark: dark)
            kept[key] = drawing
            order.append(key)
            if order.count > 30 { kept[order.removeFirst()] = nil }
            return drawing
        }
        last = Task { _ = try? await work.value }
        return try await work.value
    }

    private func draw(_ source: String, dark: Bool) async throws -> Drawing {
        let page = try await ready()
        let answer = try await page.callAsyncJavaScript("return await draw(source, dark)", arguments: ["source": source, "dark": dark], contentWorld: .page)
        guard let size = answer as? [String: Any], let width = size["width"] as? Double, let height = size["height"] as? Double,
              width > 0, height > 0 else { throw Failure.failed }
        let rect = CGRect(x: 0, y: 0, width: width, height: height)
        page.frame = rect
        let whole = WKPDFConfiguration()
        whole.rect = rect
        let pdf = try await page.pdf(configuration: whole)
        guard let image = Self.picture(pdf) else { throw Failure.failed }
        return Drawing(pdf: pdf, image: image)
    }

    /// The page, with Mermaid loaded: made the first time a diagram is drawn.
    private func ready() async throws -> WKWebView {
        if let page { return page }
        if let loading { return try await loading.value }
        let task = Task { () throws -> WKWebView in
            guard let index = Bundle.main.url(forResource: "index", withExtension: "html", subdirectory: "Diagram") else { throw Failure.failed }
            let settings = WKWebViewConfiguration()
            settings.websiteDataStore = .nonPersistent()
            let view = WKWebView(frame: CGRect(x: 0, y: 0, width: 1024, height: 768), configuration: settings)
            view.navigationDelegate = self
            try await withCheckedThrowingContinuation { (waiting: CheckedContinuation<Void, Error>) in
                loaded = waiting
                view.loadFileURL(index, allowingReadAccessTo: index.deletingLastPathComponent())
            }
            page = view
            return view
        }
        loading = task
        defer { loading = nil }
        return try await task.value
    }

    func webView(_ webView: WKWebView, didFinish navigation: WKNavigation!) {
        loaded?.resume()
        loaded = nil
    }

    func webView(_ webView: WKWebView, didFail navigation: WKNavigation!, withError error: Error) {
        loaded?.resume(throwing: error)
        loaded = nil
    }

    func webView(_ webView: WKWebView, didFailProvisionalNavigation navigation: WKNavigation!, withError error: Error) {
        loaded?.resume(throwing: error)
        loaded = nil
    }

    /// The page left its process (the system took it back): the next diagram loads it again.
    func webViewWebContentProcessDidTerminate(_ webView: WKWebView) {
        page = nil
    }

    /// A PDF's first page as a picture: for the conversation no finer than the screen, and opened
    /// large (`zoom`) fine enough to pinch into; its longer side at most `pixels`.
    static func picture(_ pdf: Data, zoom: CGFloat = 1, pixels: CGFloat = 2400) -> UIImage? {
        guard let page = PDFDocument(data: pdf)?.page(at: 0) else { return nil }
        let box = page.bounds(for: .mediaBox)
        guard box.width > 0, box.height > 0 else { return nil }
        let format = UIGraphicsImageRendererFormat.preferred()
        format.scale = min(format.scale * zoom, pixels / max(box.width, box.height))
        return UIGraphicsImageRenderer(size: box.size, format: format).image { context in
            context.cgContext.translateBy(x: 0, y: box.height)
            context.cgContext.scaleBy(x: 1, y: -1)
            page.draw(with: .mediaBox, to: context.cgContext)
        }
    }
}

/// A Mermaid diagram in a reply, as on the Mac: a bar that names it, shows
/// its source instead and copies it, then the diagram as wide as the page
/// at most. A tap opens it large; one that cannot be drawn shows its source.
struct DiagramBlock: View {
    let source: String
    let style: MarkdownStyle
    @Environment(\.colorScheme) private var scheme
    /// What was last drawn, and for which source and appearance.
    @State private var drawn: (key: String, drawing: DiagramRenderer.Drawing?)?
    @State private var showsSource = false
    @State private var opened = false
    @State private var copied = false

    /// The ground Mermaid draws on, light and dark (the Mac's).
    static let ground = Color(UIColor { $0.userInterfaceStyle == .dark ? UIColor(red: 0x18 / 255, green: 0x18 / 255, blue: 0x1b / 255, alpha: 1) : .white })

    init(source: String, style: MarkdownStyle) {
        self.source = source
        self.style = style
        // Drawn before: shown at once, in the appearance it is first laid out in.
        let dark = UITraitCollection.current.userInterfaceStyle == .dark
        _drawn = State(initialValue: DiagramRenderer.shared.cached(source, dark: dark).map { ("\(dark)\u{0}\(source)", $0) })
    }

    private var key: String { "\(scheme == .dark)\u{0}\(source)" }

    var body: some View {
        let current = drawn?.key == key ? drawn : nil
        VStack(alignment: .leading, spacing: 0) {
            bar(drawn: current?.drawing != nil)
            if showsSource || (current != nil && current?.drawing == nil) {
                ScrollView(.horizontal, showsIndicators: false) {
                    CodeText(text: source, lang: "mermaid").font(style.code).lineSpacing(4).fixedSize()
                        .padding(.horizontal, 12).padding(.vertical, 10)
                }
                .textSelection(.enabled)
                if !showsSource {
                    Text("This diagram could not be drawn.").font(.footnote).foregroundStyle(.secondary)
                        .padding(.horizontal, 12).padding(.bottom, 10)
                }
            } else if let drawing = current?.drawing {
                Button { opened = true } label: {
                    Image(uiImage: drawing.image).resizable().scaledToFit()
                        .frame(maxWidth: drawing.image.size.width, maxHeight: drawing.image.size.height)
                        .frame(maxWidth: .infinity)
                        .padding(12)
                        .contentShape(.rect)
                }
                .buttonStyle(.plain)
                .accessibilityLabel(Text("Diagram"))
                .accessibilityIdentifier("diagram-image")
            } else {
                ProgressView().frame(maxWidth: .infinity, minHeight: 120)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Self.ground, in: RoundedRectangle(cornerRadius: 10, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: 10, style: .continuous).strokeBorder(Palette.separator, lineWidth: 0.5))
        .task(id: key) {
            let key = key
            if drawn?.key == key { return }
            let drawing = try? await DiagramRenderer.shared.drawing(source, dark: scheme == .dark)
            if !Task.isCancelled { drawn = (key, drawing) }
        }
        .task(id: copied) {
            guard copied else { return }
            try? await Task.sleep(for: .seconds(1.5))
            copied = false
        }
        .sheet(isPresented: $opened) {
            if let drawing = current?.drawing { DiagramPage(pdf: drawing.pdf) }
        }
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("diagram-block")
    }

    /// The bar: with a diagram drawn, the button that shows its source in its place, and back.
    private func bar(drawn: Bool) -> some View {
        HStack(spacing: 0) {
            Text(verbatim: "Mermaid").font(.caption.monospaced())
            Spacer(minLength: 8)
            if drawn {
                Button { showsSource.toggle() } label: {
                    Text(showsSource ? "Diagram" : "Source").padding(.horizontal, 10).frame(maxHeight: .infinity).contentShape(.rect)
                }
                .buttonStyle(.plain)
                .accessibilityIdentifier("diagram-source")
            }
            Button {
                UIPasteboard.general.string = source
                copied = true
            } label: {
                Label("Copy", systemImage: copied ? "checkmark" : "doc.on.doc")
                    .padding(.horizontal, 10).frame(maxHeight: .infinity).contentShape(.rect)
            }
            .buttonStyle(.plain)
            .accessibilityLabel(copied ? Text("Copied") : Text("Copy Code"))
        }
        .font(.caption).foregroundStyle(.secondary)
        .padding(.leading, 12)
        .frame(height: 30)
        .overlay(alignment: .bottom) { Rectangle().fill(Palette.separator).frame(height: 0.5) }
    }
}

/// A diagram opened large: pinched and dragged like a photo, and shared as a document.
private struct DiagramPage: View {
    let pdf: Data
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        NavigationStack {
            Group {
                if let image = DiagramRenderer.picture(pdf, zoom: 4, pixels: 4000) { DiagramZoom(image: image) }
            }
                .ignoresSafeArea(edges: .bottom)
                .background(DiagramBlock.ground)
                .navigationTitle("Diagram")
                .navigationBarTitleDisplayMode(.inline)
                .toolbar {
                    ToolbarItem(placement: .topBarLeading) {
                        ShareLink(item: DiagramFile(pdf: pdf), preview: SharePreview(Text("Diagram")))
                    }
                    ToolbarItem(placement: .confirmationAction) {
                        Button("Done") { dismiss() }
                    }
                }
        }
        .accessibilityIdentifier("diagram-page")
    }
}

/// A diagram to share: a PDF named for what it is.
private struct DiagramFile: Transferable {
    let pdf: Data

    static var transferRepresentation: some TransferRepresentation {
        DataRepresentation(exportedContentType: .pdf) { $0.pdf }.suggestedFileName("Diagram.pdf")
    }
}

/// A picture that fills the page at rest and grows under a pinch or a double tap.
private struct DiagramZoom: UIViewRepresentable {
    let image: UIImage

    func makeCoordinator() -> Coordinator { Coordinator() }

    func makeUIView(context: Context) -> UIScrollView {
        let scroll = UIScrollView()
        scroll.delegate = context.coordinator
        scroll.maximumZoomScale = 6
        scroll.showsVerticalScrollIndicator = false
        scroll.showsHorizontalScrollIndicator = false
        let picture = UIImageView(image: image)
        picture.contentMode = .scaleAspectFit
        picture.translatesAutoresizingMaskIntoConstraints = false
        scroll.addSubview(picture)
        NSLayoutConstraint.activate([
            picture.widthAnchor.constraint(equalTo: scroll.frameLayoutGuide.widthAnchor),
            picture.heightAnchor.constraint(equalTo: scroll.frameLayoutGuide.heightAnchor),
            picture.leadingAnchor.constraint(equalTo: scroll.contentLayoutGuide.leadingAnchor),
            picture.trailingAnchor.constraint(equalTo: scroll.contentLayoutGuide.trailingAnchor),
            picture.topAnchor.constraint(equalTo: scroll.contentLayoutGuide.topAnchor),
            picture.bottomAnchor.constraint(equalTo: scroll.contentLayoutGuide.bottomAnchor),
        ])
        context.coordinator.picture = picture
        let twice = UITapGestureRecognizer(target: context.coordinator, action: #selector(Coordinator.zoom(_:)))
        twice.numberOfTapsRequired = 2
        scroll.addGestureRecognizer(twice)
        return scroll
    }

    func updateUIView(_ scroll: UIScrollView, context: Context) {}

    final class Coordinator: NSObject, UIScrollViewDelegate {
        weak var picture: UIView?

        func viewForZooming(in scrollView: UIScrollView) -> UIView? { picture }

        /// A double tap: in to where it landed, or back out.
        @objc func zoom(_ tap: UITapGestureRecognizer) {
            guard let scroll = tap.view as? UIScrollView, let picture else { return }
            if scroll.zoomScale > 1 {
                scroll.setZoomScale(1, animated: true)
            } else {
                let at = tap.location(in: picture)
                let size = CGSize(width: scroll.bounds.width / 2.5, height: scroll.bounds.height / 2.5)
                scroll.zoom(to: CGRect(x: at.x - size.width / 2, y: at.y - size.height / 2, width: size.width, height: size.height), animated: true)
            }
        }
    }
}
