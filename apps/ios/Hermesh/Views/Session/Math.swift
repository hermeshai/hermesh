import HermeshSessions
import SwiftMath
import SwiftUI

/// Math in a reply's text: its TeX, kept on the run that stands for it.
enum MathAttribute: AttributedStringKey {
    typealias Value = MathSpan
    static let name = "io.hermesh.math"
}

/// Math typeset on the phone (SwiftMath, in Latin Modern, the face nearest
/// the Mac's KaTeX) and drawn once as a picture that takes the colour of the
/// text around it.
struct MathPicture {
    let image: UIImage
    /// How far its baseline sits above its bottom edge.
    let baseline: CGFloat
}

enum MathSetter {
    private final class Box {
        let picture: MathPicture?
        init(_ picture: MathPicture?) { self.picture = picture }
    }

    private static let kept: NSCache<NSString, Box> = {
        let cache = NSCache<NSString, Box>()
        cache.countLimit = 300
        return cache
    }()

    /// The size math is set at beside text of a style: a little over the text's, as on the Mac.
    static func size(_ style: UIFont.TextStyle) -> CGFloat {
        (UIFont.preferredFont(forTextStyle: style).pointSize * 1.05).rounded()
    }

    /// The picture of some TeX, or nil when it cannot be typeset; either way worked out once.
    static func picture(_ tex: String, display: Bool, size: CGFloat) -> MathPicture? {
        let key = "\(display ? "d" : "t")\(Int(size))\u{0}\(tex)" as NSString
        if let hit = kept.object(forKey: key) { return hit.picture }
        let picture = typeset(tex, display: display, size: size)
        kept.setObject(Box(picture), forKey: key)
        return picture
    }

    private static func typeset(_ tex: String, display: Bool, size: CGFloat) -> MathPicture? {
        let label = MTMathUILabel()
        label.displayErrorInline = false
        label.labelMode = display ? .display : .text
        label.fontSize = size
        label.textColor = .black
        label.backgroundColor = .clear
        label.latex = tex
        guard label.error == nil else { return nil }
        let fit = label.intrinsicContentSize
        guard fit.width > 0, fit.height > 0 else { return nil }
        label.frame = CGRect(origin: .zero, size: CGSize(width: ceil(fit.width), height: ceil(fit.height)))
        label.layoutIfNeeded()
        guard let line = label.displayList else { return nil }
        let image = UIGraphicsImageRenderer(size: label.bounds.size).image { context in
            // The label draws for a flipped layer: turned the right way up here.
            context.cgContext.translateBy(x: 0, y: label.bounds.height)
            context.cgContext.scaleBy(x: 1, y: -1)
            label.layer.render(in: context.cgContext)
        }
        // The label centres its line; a short one stands in half a line's height.
        let height = max(line.ascent + line.descent, size / 2)
        return MathPicture(image: image.withRenderingMode(.alwaysTemplate), baseline: (label.bounds.height - height) / 2 + line.descent)
    }
}

/// A line of text with the math in it drawn: each span a picture on the
/// text's baseline, or its source in the code face when it cannot be typeset.
func mathText(_ text: AttributedString, size: CGFloat, code: Font) -> Text {
    guard text.runs.contains(where: { $0[MathAttribute.self] != nil }) else { return Text(text) }
    var out = Text(verbatim: "")
    var from = text.startIndex
    for run in text.runs {
        guard let span = run[MathAttribute.self] else { continue }
        if from < run.range.lowerBound { out = out + Text(AttributedString(text[from..<run.range.lowerBound])) }
        if let picture = MathSetter.picture(span.tex, display: false, size: size) {
            out = out + Text(Image(uiImage: picture.image)).baselineOffset(-picture.baseline)
        } else {
            out = out + Text(verbatim: span.raw).font(code)
        }
        from = run.range.upperBound
    }
    if from < text.endIndex { out = out + Text(AttributedString(text[from...])) }
    return out
}

/// Math set apart from the text, as on the Mac: centred, scrolling sideways
/// once wider than the page, and as its source when it cannot be typeset.
struct MathBlock: View {
    let tex: String
    let style: MarkdownStyle

    var body: some View {
        if let picture = MathSetter.picture(tex, display: true, size: MathSetter.size(style.size)) {
            let math = Image(uiImage: picture.image)
            ViewThatFits(in: .horizontal) {
                math
                ScrollView(.horizontal, showsIndicators: false) { math }
            }
            .frame(maxWidth: .infinity)
            .padding(.vertical, 6)
            .accessibilityElement()
            .accessibilityLabel(Text(verbatim: tex))
            .accessibilityAddTraits(.isImage)
            .accessibilityIdentifier("math-block")
        } else {
            FencedCode(text: tex, lang: "latex", font: style.code, ground: style.codeGround)
        }
    }
}
