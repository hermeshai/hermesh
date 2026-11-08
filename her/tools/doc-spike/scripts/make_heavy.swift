// A 500-page, ~50 MB report: the CoreText report plus one noisy JPEG (photo-like, ~100 KB) per page.
import AppKit
let out = CommandLine.arguments[1]
var box = CGRect(x: 0, y: 0, width: 595, height: 842)
let ctx = CGContext(URL(fileURLWithPath: out) as CFURL, mediaBox: &box, nil)!
let font = NSFont(name: "PingFangSC-Regular", size: 11)!
let para = "本公司2025年度实现营业收入1,234,567.89万元，同比增长12.5%。The Company's revenue grew 12.5% year over year. 违约金为合同总额的20%。\n"
var rng = SystemRandomNumberGenerator()
for p in 0..<500 {
    ctx.beginPDFPage(nil)
    let w = 360, h = 240
    let rep = NSBitmapImageRep(bitmapDataPlanes: nil, pixelsWide: w, pixelsHigh: h, bitsPerSample: 8, samplesPerPixel: 3, hasAlpha: false, isPlanar: false, colorSpaceName: .deviceRGB, bytesPerRow: w * 3, bitsPerPixel: 24)!
    let px = rep.bitmapData!
    for i in 0..<(w * h * 3) { px[i] = UInt8.random(in: 0...255, using: &rng) }
    let jpg = rep.representation(using: .jpeg, properties: [.compressionFactor: 0.9])!
    let img = NSBitmapImageRep(data: jpg)!.cgImage!
    ctx.draw(img, in: CGRect(x: 50, y: 60, width: 495, height: 330))
    let a = NSAttributedString(string: "第\(p + 1)页\n" + String(repeating: para, count: 12), attributes: [.font: font])
    let fs = CTFramesetterCreateWithAttributedString(a)
    CTFrameDraw(CTFramesetterCreateFrame(fs, CFRange(location: 0, length: 0), CGPath(rect: CGRect(x: 50, y: 420, width: 495, height: 370), transform: nil), nil), ctx)
    ctx.endPDFPage()
}
ctx.closePDF()
